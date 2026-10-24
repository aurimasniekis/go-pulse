package alerts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aurimasniekis/go-pulse/internal/audit"
	"github.com/aurimasniekis/go-pulse/internal/clients"
	"github.com/aurimasniekis/go-pulse/internal/notify"
	"github.com/aurimasniekis/go-pulse/internal/rollup"
	"github.com/aurimasniekis/go-pulse/internal/store"
	"github.com/aurimasniekis/go-pulse/internal/sysapi"
	"github.com/aurimasniekis/go-pulse/internal/testutil/natstest"
	"github.com/aurimasniekis/go-pulse/pkg/pulse/envelope"
)

type fakeSender struct {
	mu   sync.Mutex
	msgs []notify.Message
}

func (f *fakeSender) Kind() string { return "telegram" }
func (f *fakeSender) Send(_ context.Context, m notify.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = append(f.msgs, m)
	return nil
}

func (f *fakeSender) wait(t *testing.T, n int) []notify.Message {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		if len(f.msgs) >= n {
			out := append([]notify.Message(nil), f.msgs...)
			f.mu.Unlock()
			return out
		}
		f.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("got %d notifications, want %d", len(f.msgs), n)
	return nil
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type rig struct {
	e      *Engine
	db     *store.DB
	clk    *clock
	sender *fakeSender
	certs  []Cert
	up     bool
	mu     sync.Mutex
}

func newRig(t *testing.T, o Options) *rig {
	t.Helper()
	db, err := store.Open(context.Background(), store.Options{Path: filepath.Join(t.TempDir(), "pulse.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	r := &rig{db: db, clk: &clock{t: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)}, sender: &fakeSender{}, up: true}
	n := notify.New("tg", r.sender, notify.Options{MinSeverity: notify.Info, PerMinute: 600, Burst: 100})
	o.Notifiers = append(o.Notifiers, n)
	o.Now = r.clk.now
	p := Providers{
		Rollup:        rollup.New(nil, db, rollup.Options{}),
		Certs:         func() []Cert { r.mu.Lock(); defer r.mu.Unlock(); return append([]Cert(nil), r.certs...) },
		NATSConnected: func() bool { r.mu.Lock(); defer r.mu.Unlock(); return r.up },
	}
	r.e = New(db, p, o)
	if err := r.e.Seed(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go n.Run(ctx)
	return r
}

func (r *rig) rule(t *testing.T, id string) *AlertRule {
	t.Helper()
	ru, err := r.e.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return ru
}

func TestSeedDefaults(t *testing.T) {
	r := newRig(t, Options{})
	rules, err := r.e.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != len(builtins) {
		t.Fatalf("rules = %d", len(rules))
	}
	ce := r.rule(t, KindCertExpiry)
	if !ce.Builtin || !ce.Enabled || ce.Severity != notify.Critical || ce.Params.Days != 14 || ce.Description == "" {
		t.Fatalf("%+v", ce)
	}
	// Seeding again keeps operator changes.
	if _, err := r.e.Update(context.Background(), KindCertExpiry, RuleInput{Enabled: true, Severity: notify.Warning, Params: RuleParams{Days: 21}}, audit.Actor{Name: "t"}); err != nil {
		t.Fatal(err)
	}
	_ = r.e.Seed(context.Background())
	if ce := r.rule(t, KindCertExpiry); ce.Params.Days != 21 || ce.Severity != notify.Warning {
		t.Fatalf("seed overwrote settings: %+v", ce)
	}
	if err := r.e.Delete(context.Background(), KindCertExpiry, audit.Actor{Name: "t"}); !errors.Is(err, ErrBuiltin) {
		t.Fatalf("deleting a builtin: %v", err)
	}
}

func TestCertExpiryFiresAndResolves(t *testing.T) {
	r := newRig(t, Options{})
	ctx := context.Background()
	r.certs = []Cert{{Name: "NATS Let's Encrypt", NotAfter: r.clk.now().Add(5 * 24 * time.Hour)}, {Name: "Internal CA", NotAfter: r.clk.now().AddDate(10, 0, 0)}}
	r.e.Evaluate(ctx)
	ce := r.rule(t, KindCertExpiry)
	if len(ce.Instances) != 1 || ce.Instances[0].State != "firing" || ce.Instances[0].Key != "NATS Let's Encrypt" || ce.Instances[0].Value != 5 {
		t.Fatalf("%+v", ce.Instances)
	}
	msgs := r.sender.wait(t, 1)
	if msgs[0].State != "firing" || msgs[0].Severity != notify.Critical || !strings.Contains(msgs[0].Text, "expires in 5.0 days") {
		t.Fatalf("%+v", msgs[0])
	}
	// Still true: no new transition.
	r.e.Evaluate(ctx)
	r.mu.Lock()
	r.certs[0].NotAfter = r.clk.now().Add(80 * 24 * time.Hour)
	r.mu.Unlock()
	r.e.Evaluate(ctx)
	if ce := r.rule(t, KindCertExpiry); len(ce.Instances) != 0 {
		t.Fatalf("still firing: %+v", ce.Instances)
	}
	msgs = r.sender.wait(t, 2)
	if len(msgs) != 2 || msgs[1].State != "resolved" {
		t.Fatalf("%+v", msgs)
	}
	h, err := r.e.History(ctx, HistoryQuery{RuleID: KindCertExpiry})
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != 2 || h[0].State != "resolved" || h[1].State != "firing" {
		t.Fatalf("history %+v", h)
	}
	if f := r.e.Firing(ctx); f[notify.Critical] != 0 {
		t.Fatalf("firing %v", f)
	}
}

func TestForSecondsAndReminder(t *testing.T) {
	r := newRig(t, Options{Repeat: time.Hour})
	ctx := context.Background()
	r.up = false
	r.e.Evaluate(ctx) // nats_down holds 60 s before firing
	nd := r.rule(t, KindNATSDown)
	if len(nd.Instances) != 1 || nd.Instances[0].State != "pending" {
		t.Fatalf("%+v", nd.Instances)
	}
	r.clk.add(61 * time.Second)
	r.e.Evaluate(ctx)
	if nd := r.rule(t, KindNATSDown); nd.Instances[0].State != "firing" {
		t.Fatalf("%+v", nd.Instances)
	}
	r.sender.wait(t, 1)
	r.clk.add(30 * time.Minute)
	r.e.Evaluate(ctx)
	r.clk.add(31 * time.Minute)
	r.e.Evaluate(ctx)
	msgs := r.sender.wait(t, 2)
	if msgs[1].State != "reminder" {
		t.Fatalf("%+v", msgs)
	}
	if h, _ := r.e.History(ctx, HistoryQuery{RuleID: KindNATSDown}); len(h) != 1 {
		t.Fatalf("reminders must not write history: %+v", h)
	}
	// A pending condition that clears before for_seconds never notifies.
	r.up = true
	r.e.Evaluate(ctx)
	r.sender.wait(t, 3) // resolved
	r.up = false
	r.e.Evaluate(ctx)
	r.up = true
	r.e.Evaluate(ctx)
	time.Sleep(50 * time.Millisecond)
	if msgs := r.sender.wait(t, 3); len(msgs) != 3 {
		t.Fatalf("pending flap notified: %+v", msgs)
	}
}

func TestDisableClearsState(t *testing.T) {
	r := newRig(t, Options{})
	ctx := context.Background()
	r.certs = []Cert{{Name: "x", NotAfter: r.clk.now().Add(time.Hour)}}
	r.e.Evaluate(ctx)
	ru, err := r.e.Update(ctx, KindCertExpiry, RuleInput{Enabled: false, Severity: notify.Critical, Params: RuleParams{Days: 14}}, audit.Actor{Name: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if ru.Enabled || len(ru.Instances) != 0 {
		t.Fatalf("%+v", ru)
	}
	h, _ := r.e.History(ctx, HistoryQuery{RuleID: KindCertExpiry})
	if len(h) != 2 || h[0].Summary != "rule disabled" {
		t.Fatalf("%+v", h)
	}
	r.e.Evaluate(ctx)
	if ru := r.rule(t, KindCertExpiry); len(ru.Instances) != 0 {
		t.Fatal("disabled rule evaluated")
	}
}

func TestNoDataKeepsState(t *testing.T) {
	r := newRig(t, Options{})
	r.e.Evaluate(context.Background())
	ru := r.rule(t, KindBackupStale)
	if ru.NoData == "" || len(ru.Instances) != 0 || ru.LastEval.IsZero() {
		t.Fatalf("%+v", ru)
	}
}

func seedRollup(t *testing.T, db *store.DB, dim, key string, bucket, msgs int64) {
	t.Helper()
	if err := db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `INSERT INTO rollup_1m(stream, dim, key, bucket, msgs, bytes) VALUES ('EVENTS', ?, ?, ?, ?, ?)`,
			dim, key, bucket, msgs, msgs*100)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestThresholdRules(t *testing.T) {
	r := newRig(t, Options{})
	ctx := context.Background()
	actor := audit.Actor{Name: "t"}
	// Dead-man switch: fewer than 1 home event in 10 minutes.
	quiet, err := r.e.Create(ctx, RuleInput{Name: "Home quiet", Enabled: true, Severity: notify.Warning,
		Params: RuleParams{Dimension: "prefix", Key: "evt.home.>", Op: "below", Threshold: 1, WindowMinutes: 10}}, actor)
	if err != nil {
		t.Fatal(err)
	}
	if quiet.Params.Key != "evt.home.>" || quiet.Params.Stream != "EVENTS" || !strings.HasPrefix(quiet.ID, "custom_") || !strings.Contains(quiet.Description, "below 1") {
		t.Fatalf("%+v", quiet)
	}
	burst, err := r.e.Create(ctx, RuleInput{Name: "Too many errors", Enabled: true, Severity: notify.Critical,
		Params: RuleParams{Dimension: "type", Key: "app.error.v1", Op: "above", Threshold: 5, WindowMinutes: 5}}, actor)
	if err != nil {
		t.Fatal(err)
	}
	now := r.clk.now().Unix()
	cur := now - now%60
	seedRollup(t, r.db, "type", "app.error.v1", cur-120, 6)
	seedRollup(t, r.db, "type", "app.error.v1", cur, 100) // current (incomplete) bucket is ignored
	r.e.Evaluate(ctx)
	if q := r.rule(t, quiet.ID); len(q.Instances) != 1 || q.Instances[0].Value != 0 {
		t.Fatalf("quiet %+v", q.Instances)
	}
	if b := r.rule(t, burst.ID); len(b.Instances) != 1 || b.Instances[0].Value != 6 {
		t.Fatalf("burst %+v", b.Instances)
	}
	seedRollup(t, r.db, "p2", "evt.home", cur-60, 3)
	r.e.Evaluate(ctx)
	if q := r.rule(t, quiet.ID); len(q.Instances) != 0 {
		t.Fatalf("quiet still firing %+v", q.Instances)
	}
	if err := r.e.Delete(ctx, quiet.ID, actor); err != nil {
		t.Fatal(err)
	}
	if _, err := r.e.Get(ctx, quiet.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestValidate(t *testing.T) {
	bad := []RuleParams{
		{Dimension: "prefix", Key: "evt.home.door", Op: "below", Threshold: 1, WindowMinutes: 5}, // depth 3 → hourly
		{Dimension: "prefix", Key: "evt.*", Op: "below", WindowMinutes: 5},
		{Dimension: "prefix", Key: "evt.a.b.c.d", Op: "below", WindowMinutes: 1440},
		{Dimension: "type", Op: "above", WindowMinutes: 5},
		{Dimension: "total", Op: "sideways", WindowMinutes: 5},
		{Dimension: "total", Op: "above", WindowMinutes: 0},
	}
	for i, p := range bad {
		if err := Validate(KindThreshold, &p, 0); err == nil {
			t.Errorf("case %d accepted: %+v", i, p)
		}
	}
	ok := RuleParams{Dimension: "prefix", Key: "evt.home.door", Op: "below", Threshold: 1, WindowMinutes: 120}
	if err := Validate(KindThreshold, &ok, 0); err != nil {
		t.Fatal(err)
	}
	if err := Validate(KindStreamUsage, &RuleParams{Ratio: 1.5}, 0); err == nil {
		t.Fatal("ratio 1.5 accepted")
	}
}

func TestClientExpiryAndPrune(t *testing.T) {
	r := newRig(t, Options{})
	ctx := context.Background()
	now := r.clk.now()
	if err := r.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO clients(id, name, user_key, signing_key, status, expires_at, created_at, updated_at)
			VALUES ('c1', 'door-bot', 'U1', 'A1', 'active', ?, 0, 0), ('c2', 'old', 'U2', 'A2', 'revoked', ?, 0, 0), ('c3', 'fresh', 'U3', 'A3', 'active', ?, 0, 0)`,
			store.Ms(now.Add(10*24*time.Hour)), store.Ms(now.Add(time.Hour)), store.Ms(now.AddDate(1, 0, 0)))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	r.e.Evaluate(ctx)
	ce := r.rule(t, KindClientExpiry)
	if len(ce.Instances) != 1 || ce.Instances[0].Key != "door-bot" {
		t.Fatalf("%+v", ce.Instances)
	}
	r.clk.add(HistoryRetention + time.Hour)
	if err := r.e.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	if h, _ := r.e.History(ctx, HistoryQuery{}); len(h) != 0 {
		t.Fatalf("history not pruned: %d", len(h))
	}
}

func TestAlertEventPublished(t *testing.T) {
	env := natstest.Start(t, natstest.Options{NoWebsocket: true})
	r := newRig(t, Options{})
	if _, err := clients.New(r.db, env.Keys, sysapi.New(env.SysConn()), nil).Sync(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	nc := env.AppConn()
	sub, err := nc.SubscribeSync(SubjectPrefix + ">")
	if err != nil {
		t.Fatal(err)
	}
	_ = nc.Flush()
	r.e.o.Publish = nc
	r.certs = []Cert{{Name: "origin", NotAfter: r.clk.now().Add(24 * time.Hour)}}
	r.e.Evaluate(context.Background())
	m, err := sub.NextMsg(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if m.Subject != "_sys.alert.cert_expiry" || m.Header.Get(envelope.HeaderType) != TypeFiring || m.Header.Get(envelope.HeaderSource) != "pulsed" ||
		m.Header.Get(envelope.HeaderID) == "" {
		t.Fatalf("%s %v", m.Subject, m.Header)
	}
	var ev Event
	if err := json.Unmarshal(m.Data, &ev); err != nil || ev.Key != "origin" || ev.State != "firing" || ev.Severity != notify.Critical {
		t.Fatalf("%s %v", m.Data, err)
	}
}
