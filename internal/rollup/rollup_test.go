package rollup

import (
	"context"
	"database/sql"
	"slices"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/aurimasniekis/go-pulse/internal/clients"
	"github.com/aurimasniekis/go-pulse/internal/store"
	"github.com/aurimasniekis/go-pulse/internal/streams"
	"github.com/aurimasniekis/go-pulse/internal/sysapi"
	"github.com/aurimasniekis/go-pulse/internal/testutil/natstest"
)

type fx struct {
	js jetstream.JetStream
	db *store.DB
}

func setup(t *testing.T) *fx {
	t.Helper()
	env := natstest.Start(t)
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{Path: env.Layout.DatabaseFile()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := clients.New(db, env.Keys, sysapi.New(env.SysConn()), nil).Sync(ctx, false); err != nil {
		t.Fatal(err)
	}
	js, _ := jetstream.New(env.AppConn())
	if _, err := streams.EnsureEvents(ctx, js, 256<<20, false, nil); err != nil {
		t.Fatal(err)
	}
	return &fx{js: js, db: db}
}

func (f *fx) pub(t *testing.T, subject, typ, data string, n int) {
	t.Helper()
	for range n {
		m := nats.NewMsg(subject)
		m.Data = []byte(data)
		if typ != "" {
			m.Header.Set("ce-type", typ)
		}
		if _, err := f.js.PublishMsg(context.Background(), m); err != nil {
			t.Fatal(err)
		}
	}
}

// start runs an engine until the returned stop func is called (which waits for the final flush).
func (f *fx) start(t *testing.T, o Options) (*Engine, func()) {
	t.Helper()
	o.Streams = []string{streams.Events}
	if o.FlushEvery == 0 {
		o.FlushEvery = 50 * time.Millisecond
	}
	e := New(f.js, f.db, o)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	return e, func() { cancel(); <-done }
}

func (f *fx) waitCheckpoint(t *testing.T, seq uint64) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var got int64
		err := f.db.R().QueryRow("SELECT last_seq FROM rollup_checkpoints WHERE stream = 'EVENTS'").Scan(&got)
		if err == nil && uint64(got) >= seq {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("checkpoint did not reach %d", seq)
}

func (f *fx) sum(t *testing.T, table, dim, key string) int64 {
	t.Helper()
	var n int64
	if err := f.db.R().QueryRow("SELECT COALESCE(SUM(msgs), 0) FROM "+table+" WHERE stream = 'EVENTS' AND dim = ? AND key = ?", dim, key).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCountsExactlyOnceAcrossRestart(t *testing.T) {
	f := setup(t)
	f.pub(t, "evt.home.door.opened", "home.door.opened.v1", `{"door":"front","meta":{"by":"me"}}`, 50)
	f.pub(t, "evt.home.light.on", "home.light.on.v1", `{}`, 40)
	f.pub(t, "evt.work.build.done.x", "", "raw", 30)
	_, stop := f.start(t, Options{})
	f.waitCheckpoint(t, 120)
	stop()

	for _, c := range []struct {
		table, dim, key string
		want            int64
	}{
		{"rollup_1m", DimTotal, "", 120}, {"rollup_1h", DimTotal, "", 120}, {"rollup_1d", DimTotal, "", 120},
		{"rollup_1m", "p1", "evt", 120}, {"rollup_1m", "p2", "evt.home", 90}, {"rollup_1m", "p2", "evt.work", 30},
		{"rollup_1m", "p3", "evt.home.door", 0}, // minute table stops at depth 2
		{"rollup_1h", "p3", "evt.home.door", 50}, {"rollup_1h", "p4", "evt.home.door.opened", 0},
		{"rollup_1d", "p4", "evt.home.door.opened", 50}, {"rollup_1d", "p4", "evt.work.build.done", 30},
		{"rollup_1d", DimType, "home.door.opened.v1", 50}, {"rollup_1d", DimType, "home.light.on.v1", 40},
		{"rollup_1m", DimType, Untyped, 30},
	} {
		if got := f.sum(t, c.table, c.dim, c.key); got != c.want {
			t.Errorf("%s %s %q = %d want %d", c.table, c.dim, c.key, got, c.want)
		}
	}
	var b1, b2 int64
	_ = f.db.R().QueryRow("SELECT SUM(bytes) FROM rollup_1m WHERE dim='total'").Scan(&b1)
	_ = f.db.R().QueryRow("SELECT SUM(bytes) FROM rollup_1d WHERE dim='total'").Scan(&b2)
	if b1 == 0 || b1 != b2 {
		t.Fatalf("bytes 1m=%d 1d=%d", b1, b2)
	}

	// A new engine resumes from the checkpoint: only new events are added.
	f.pub(t, "evt.home.door.opened", "home.door.opened.v1", `{}`, 10)
	_, stop = f.start(t, Options{})
	f.waitCheckpoint(t, 130)
	stop()
	if got := f.sum(t, "rollup_1d", DimTotal, ""); got != 130 {
		t.Fatalf("after restart total = %d, want 130", got)
	}
	var subjMsgs int64
	var lastSeq int64
	_ = f.db.R().QueryRow("SELECT msgs, last_seq FROM subjects WHERE subject = 'evt.home.door.opened'").Scan(&subjMsgs, &lastSeq)
	if subjMsgs != 60 || lastSeq != 130 {
		t.Fatalf("subject index msgs=%d last_seq=%d", subjMsgs, lastSeq)
	}
}

func TestCrashLosesNothingAndCountsOnce(t *testing.T) {
	f := setup(t)
	f.pub(t, "evt.a.b.c", "t", "{}", 20)
	e, _ := f.start(t, Options{FlushEvery: time.Hour}) // never flushes on its own
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if p := e.Cache().Series("EVENTS", time.Now().Add(-time.Minute).Unix(), time.Now().Unix()+60); sumPoints(p) == 20 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Crash: stop delivery without the final flush.
	e.mu.Lock()
	for _, w := range e.workers {
		w.cc.Stop()
	}
	e.mu.Unlock()
	if got := f.sum(t, "rollup_1m", DimTotal, ""); got != 0 {
		t.Fatalf("committed before crash: %d", got)
	}
	_, stop := f.start(t, Options{})
	f.waitCheckpoint(t, 20)
	stop()
	if got := f.sum(t, "rollup_1m", DimTotal, ""); got != 20 {
		t.Fatalf("after crash recovery: %d", got)
	}
}

func sumPoints(ps []Point) int64 {
	var n int64
	for _, p := range ps {
		n += p.Msgs
	}
	return n
}

func TestSubjectCapAndTree(t *testing.T) {
	f := setup(t)
	f.pub(t, "evt.home.door.opened", "door", "{}", 3)
	f.pub(t, "evt.home.light.on", "light", "{}", 2)
	f.pub(t, "evt.work.build.done", "build", "{}", 1)
	e, stop := f.start(t, Options{SubjectCap: 2})
	f.waitCheckpoint(t, 6)
	stop()
	n, dropped, err := e.IndexStats(context.Background(), "EVENTS")
	if err != nil || n != 2 || dropped != 1 {
		t.Fatalf("index %d dropped %d %v", n, dropped, err)
	}

	f2 := setup(t)
	f2.pub(t, "evt.home.door.opened", "door", "{}", 3)
	f2.pub(t, "evt.home.door.opened.front", "door", "{}", 1)
	f2.pub(t, "evt.home.light.on", "light", "{}", 2)
	f2.pub(t, "evt.work.build.done", "build", "{}", 1)
	e2, stop2 := f2.start(t, Options{})
	f2.waitCheckpoint(t, 7)
	stop2()
	ctx := context.Background()
	roots, _ := e2.Children(ctx, "EVENTS", "")
	if len(roots) != 1 || roots[0].Token != "evt" || roots[0].Subjects != 4 || roots[0].Msgs != 7 || roots[0].Children != 2 {
		t.Fatalf("roots %+v", roots)
	}
	lvl, _ := e2.Children(ctx, "EVENTS", "evt")
	if len(lvl) != 2 || lvl[0].Token != "home" || lvl[0].Msgs != 6 || lvl[0].Children != 2 || lvl[1].Token != "work" {
		t.Fatalf("evt children %+v", lvl)
	}
	door, _ := e2.Children(ctx, "EVENTS", "evt.home.door")
	if len(door) != 1 || !door[0].Leaf || door[0].Children != 1 || door[0].Msgs != 4 || door[0].Path != "evt.home.door.opened" {
		t.Fatalf("door %+v", door)
	}
	// LIKE wildcards in the prefix are escaped.
	if odd, _ := e2.Children(ctx, "EVENTS", "evt.h_me"); len(odd) != 0 {
		t.Fatalf("underscore matched: %+v", odd)
	}
}

func TestSampleKeysAndPrune(t *testing.T) {
	f := setup(t)
	f.pub(t, "evt.home.door.opened", "door", `{"door":"front","meta":{"by":"me","deep":{"x":1}}}`, 1)
	e, stop := f.start(t, Options{})
	f.waitCheckpoint(t, 1)
	stop()
	ctx := context.Background()
	if err := e.SampleKeys(ctx); err != nil {
		t.Fatal(err)
	}
	s, err := e.Subject(ctx, "EVENTS", "evt.home.door.opened")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(s.JSONKeys, []string{"door", "meta", "meta.by", "meta.deep"}) || s.KeysAt == nil {
		t.Fatalf("keys %v", s.JSONKeys)
	}
	old := time.Now().Add(-9 * 24 * time.Hour).Unix()
	if err := f.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO rollup_1m(stream, dim, key, bucket, msgs, bytes) VALUES ('EVENTS', 'total', '', ?, 1, 1)", old-old%60)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = f.db.R().QueryRow("SELECT COUNT(*) FROM rollup_1m WHERE bucket < ?", time.Now().Add(-8*24*time.Hour).Unix()).Scan(&n)
	if n != 0 {
		t.Fatal("old minute bucket survived pruning")
	}
}

func TestSeriesTopAndForecast(t *testing.T) {
	f := setup(t)
	f.pub(t, "evt.a.b.c", "t1", "0123456789", 5)
	f.pub(t, "evt.x.y.z", "t2", "0123456789", 3)
	e, stop := f.start(t, Options{})
	f.waitCheckpoint(t, 8)
	stop()
	ctx := context.Background()
	from, to := Ranges["1h"].Bounds(time.Now())
	pts, err := e.Series(ctx, "EVENTS", Minute, DimTotal, "", from, to)
	if err != nil || len(pts) != 60 || sumPoints(pts) != 8 {
		t.Fatalf("series len %d sum %d %v", len(pts), sumPoints(pts), err)
	}
	top, _ := e.Top(ctx, "EVENTS", Minute, DimType, from, to, 10)
	if len(top) != 2 || top[0].Key != "t1" || top[0].Msgs != 5 {
		t.Fatalf("top %+v", top)
	}
	avg, days, err := e.AverageDaily(ctx, "EVENTS")
	if err != nil || avg <= 0 || days <= 0 {
		t.Fatalf("avg %d days %f %v", avg, days, err)
	}
}

func TestComputeForecast(t *testing.T) {
	now := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	f := ComputeForecast(10<<30, 60<<30, 1<<30, 30, now)
	if *f.HorizonDays != 60 || f.FullAt == nil || !f.FullAt.Equal(now.Add(50*24*time.Hour)) || f.DiscardingOld {
		t.Fatalf("%+v", f)
	}
	full := ComputeForecast(60<<30, 60<<30, 1<<30, 30, now)
	if !full.DiscardingOld || full.FullAt != nil || full.UsedRatio != 1 {
		t.Fatalf("full %+v", full)
	}
	idle := ComputeForecast(1, 60<<30, 0, 0, now)
	if idle.HorizonDays != nil || idle.FullAt != nil {
		t.Fatalf("idle %+v", idle)
	}
}

func TestPrefixesAndJSONKeys(t *testing.T) {
	if got := prefixes("evt.a.b.c.d", 4); !slices.Equal(got, []string{"evt", "evt.a", "evt.a.b", "evt.a.b.c"}) {
		t.Fatal(got)
	}
	if got := prefixes("evt.a", 4); !slices.Equal(got, []string{"evt", "evt.a"}) {
		t.Fatal(got)
	}
	if JSONKeys([]byte(`[1,2]`), 2) != nil || JSONKeys([]byte(`nope`), 2) != nil {
		t.Fatal("non-object payloads produced keys")
	}
}
