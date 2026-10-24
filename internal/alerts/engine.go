package alerts

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/aurimasniekis/go-pulse/internal/audit"
	"github.com/aurimasniekis/go-pulse/internal/metrics"
	"github.com/aurimasniekis/go-pulse/internal/notify"
	"github.com/aurimasniekis/go-pulse/internal/rollup"
	"github.com/aurimasniekis/go-pulse/internal/store"
	"github.com/aurimasniekis/go-pulse/internal/sysapi"
	"github.com/aurimasniekis/go-pulse/pkg/pulse/envelope"
)

// Errors.
var (
	ErrNotFound = errors.New("alert rule not found")
	ErrBuiltin  = errors.New("built-in rules cannot be deleted; disable them instead")
	ErrInvalid  = errors.New("invalid rule")
)

// SubjectPrefix is where alert events are published (core NATS, APP account).
const SubjectPrefix = "_sys.alert."

// HistoryRetention is how long alert history is kept (PL-DB-005).
const HistoryRetention = 90 * 24 * time.Hour

// Cert is a certificate the cert_expiry rule watches.
type Cert struct {
	Name     string
	NotAfter time.Time
}

// BackupStatus is what backup_stale reads (M11).
type BackupStatus struct {
	LastSuccess time.Time
	LastError   string
}

// Providers supply live data; nil providers make their rule report "no data" instead of firing.
type Providers struct {
	JS            jetstream.JetStream
	Sys           *sysapi.Client
	Rollup        *rollup.Engine
	Certs         func() []Cert
	Backup        func(ctx context.Context) (BackupStatus, bool)
	NATSConnected func() bool
}

// Options configure the engine.
type Options struct {
	Interval  time.Duration // evaluation interval, default 1m
	Repeat    time.Duration // reminder interval while firing, default 24h; negative disables
	Publish   *nats.Conn    // where _sys.alert.* events go; nil disables
	Notifiers []*notify.Notifier
	Log       *slog.Logger
	Now       func() time.Time
}

// AlertRule is a rule with its live state.
type AlertRule struct {
	ID          string          `json:"id"`
	Kind        string          `json:"kind"`
	Builtin     bool            `json:"builtin"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Enabled     bool            `json:"enabled"`
	Severity    string          `json:"severity" enum:"info,warning,critical"`
	Params      RuleParams      `json:"params"`
	ForSeconds  int             `json:"for_seconds" doc:"The condition must hold this long before the alert fires"`
	Instances   []AlertInstance `json:"instances" doc:"Keys whose condition holds now (pending or firing)"`
	LastEval    time.Time       `json:"last_eval,omitzero"`
	LastError   string          `json:"last_error,omitempty" doc:"The last evaluation failed; the state was left unchanged"`
	NoData      string          `json:"no_data,omitempty" doc:"Why the rule cannot evaluate (e.g. backups are not configured)"`
}

// AlertInstance is one (rule, key) whose condition holds.
type AlertInstance struct {
	Key     string    `json:"key"`
	State   string    `json:"state" enum:"pending,firing"`
	Since   time.Time `json:"since"`
	FiredAt time.Time `json:"fired_at,omitzero"`
	Value   float64   `json:"value"`
	Summary string    `json:"summary"`
}

// AlertHistoryEntry is one firing or resolved transition.
type AlertHistoryEntry struct {
	ID       int64     `json:"id"`
	Time     time.Time `json:"time"`
	RuleID   string    `json:"rule_id"`
	RuleName string    `json:"rule_name"`
	Key      string    `json:"key"`
	State    string    `json:"state" enum:"firing,resolved"`
	Severity string    `json:"severity"`
	Summary  string    `json:"summary"`
	Value    float64   `json:"value"`
}

// RuleInput creates or updates a rule. Built-in rules accept only enabled, severity, params and for_seconds.
type RuleInput struct {
	Name       string     `json:"name,omitempty" maxLength:"100"`
	Enabled    bool       `json:"enabled"`
	Severity   string     `json:"severity" enum:"info,warning,critical"`
	Params     RuleParams `json:"params"`
	ForSeconds int        `json:"for_seconds,omitempty"`
}

// Finding is a condition that holds for a key.
type Finding struct {
	Key     string
	Value   float64
	Summary string
}

type evalInfo struct {
	at     time.Time
	err    string
	noData string
}

// Engine evaluates rules.
type Engine struct {
	db *store.DB
	p  Providers
	o  Options

	mu    sync.Mutex
	evals map[string]evalInfo
	slow  []slowSample
	run   sync.Mutex // serialises evaluations
}

// New returns an engine; call Seed before Run.
func New(db *store.DB, p Providers, o Options) *Engine {
	if o.Interval <= 0 {
		o.Interval = time.Minute
	}
	if o.Repeat == 0 {
		o.Repeat = 24 * time.Hour
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Engine{db: db, p: p, o: o, evals: map[string]evalInfo{}}
}

// Notifiers returns the configured notifiers.
func (e *Engine) Notifiers() []*notify.Notifier { return e.o.Notifiers }

// Seed inserts missing built-in rules with their defaults (existing rows keep the operator's settings).
func (e *Engine) Seed(ctx context.Context) error {
	now := store.Ms(e.o.Now())
	return e.db.Write(ctx, func(tx *sql.Tx) error {
		for _, b := range builtins {
			params, _ := json.Marshal(b.params)
			if _, err := tx.ExecContext(ctx, `INSERT INTO alert_rules(id, kind, builtin, name, enabled, severity, params, for_seconds, created_at, updated_at)
				VALUES (?, ?, 1, ?, 1, ?, ?, ?, ?, ?) ON CONFLICT(id) DO NOTHING`, b.kind, b.kind, b.name, b.severity, string(params), b.forSeconds, now, now); err != nil {
				return err
			}
		}
		return nil
	})
}

type ruleRow struct {
	AlertRule
	paramsJSON string
}

func (e *Engine) loadRules(ctx context.Context, where string, args ...any) ([]*AlertRule, error) {
	rows, err := e.db.R().QueryContext(ctx, `SELECT id, kind, builtin, name, enabled, severity, params, for_seconds FROM alert_rules `+where+
		` ORDER BY builtin DESC, name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*AlertRule
	for rows.Next() {
		var r ruleRow
		if err := rows.Scan(&r.ID, &r.Kind, &r.Builtin, &r.Name, &r.Enabled, &r.Severity, &r.paramsJSON, &r.ForSeconds); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(r.paramsJSON), &r.Params)
		if b, ok := builtinOf(r.Kind); ok && r.Builtin {
			r.Description = b.desc
		} else {
			r.Description = describeThreshold(r.Params)
		}
		r.Instances = []AlertInstance{}
		rr := r.AlertRule
		out = append(out, &rr)
	}
	return out, rows.Err()
}

func describeThreshold(p RuleParams) string {
	what := "events"
	if p.Metric == "bytes" {
		what = "payload bytes"
	}
	scope := "all of " + p.Stream
	switch p.Dimension {
	case "type":
		scope = "type " + p.Key + " in " + p.Stream
	case "prefix":
		scope = p.Key + ".> in " + p.Stream
	}
	return fmt.Sprintf("Fires when %s for %s over %d min is %s %g.", what, scope, p.WindowMinutes, p.Op, p.Threshold)
}

func (e *Engine) attachState(ctx context.Context, rules []*AlertRule) error {
	byID := map[string]*AlertRule{}
	for _, r := range rules {
		byID[r.ID] = r
		e.mu.Lock()
		ev := e.evals[r.ID]
		e.mu.Unlock()
		r.LastEval, r.LastError, r.NoData = ev.at, ev.err, ev.noData
	}
	rows, err := e.db.R().QueryContext(ctx, `SELECT rule_id, key, state, since, fired_at, value, summary FROM alert_state ORDER BY since`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var in AlertInstance
		var since, fired int64
		if err := rows.Scan(&id, &in.Key, &in.State, &since, &fired, &in.Value, &in.Summary); err != nil {
			return err
		}
		in.Since = store.FromMs(since).UTC()
		if fired > 0 {
			in.FiredAt = store.FromMs(fired).UTC()
		}
		if r := byID[id]; r != nil {
			r.Instances = append(r.Instances, in)
		}
	}
	return rows.Err()
}

// List returns every rule with its instances.
func (e *Engine) List(ctx context.Context) ([]*AlertRule, error) {
	rules, err := e.loadRules(ctx, "")
	if err != nil {
		return nil, err
	}
	return rules, e.attachState(ctx, rules)
}

// Get returns one rule.
func (e *Engine) Get(ctx context.Context, id string) (*AlertRule, error) {
	rules, err := e.loadRules(ctx, "WHERE id = ?", id)
	if err != nil {
		return nil, err
	}
	if len(rules) == 0 {
		return nil, ErrNotFound
	}
	return rules[0], e.attachState(ctx, rules)
}

// Firing counts firing instances by severity (metrics).
func (e *Engine) Firing(ctx context.Context) map[string]int {
	out := map[string]int{}
	rows, err := e.db.R().QueryContext(ctx, `SELECT r.severity, COUNT(*) FROM alert_state s JOIN alert_rules r ON r.id = s.rule_id
		WHERE s.state = 'firing' GROUP BY r.severity`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var sev string
		var n int
		if rows.Scan(&sev, &n) == nil {
			out[sev] = n
		}
	}
	if rows.Err() != nil {
		return map[string]int{}
	}
	return out
}

func checkSeverity(s string) error {
	if notify.Rank(s) == 0 {
		return fmt.Errorf("%w: severity must be info, warning or critical", ErrInvalid)
	}
	return nil
}

func newCustomID() string {
	b := make([]byte, 10)
	_, _ = rand.Read(b)
	return "custom_" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
}

// Create adds a custom threshold rule.
func (e *Engine) Create(ctx context.Context, in RuleInput, actor audit.Actor) (*AlertRule, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return nil, fmt.Errorf("%w: name is required", ErrInvalid)
	}
	if err := checkSeverity(in.Severity); err != nil {
		return nil, err
	}
	if err := Validate(KindThreshold, &in.Params, in.ForSeconds); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	id := newCustomID()
	params, _ := json.Marshal(in.Params)
	now := store.Ms(e.o.Now())
	if err := e.db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO alert_rules(id, kind, builtin, name, enabled, severity, params, for_seconds, created_at, updated_at)
			VALUES (?, ?, 0, ?, ?, ?, ?, ?, ?, ?)`, id, KindThreshold, in.Name, in.Enabled, in.Severity, string(params), in.ForSeconds, now, now); err != nil {
			return err
		}
		return audit.Insert(ctx, tx, actor, "alert.rule.create", id, audit.OK, map[string]any{"name": in.Name})
	}); err != nil {
		return nil, err
	}
	return e.Get(ctx, id)
}

// Update changes a rule. Disabling clears its state (recorded as resolved, without notifications).
func (e *Engine) Update(ctx context.Context, id string, in RuleInput, actor audit.Actor) (*AlertRule, error) {
	cur, err := e.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := checkSeverity(in.Severity); err != nil {
		return nil, err
	}
	name := cur.Name
	if !cur.Builtin {
		if n := strings.TrimSpace(in.Name); n != "" {
			name = n
		}
	}
	if err := Validate(cur.Kind, &in.Params, in.ForSeconds); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	params, _ := json.Marshal(in.Params)
	now := e.o.Now()
	if err := e.db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE alert_rules SET name = ?, enabled = ?, severity = ?, params = ?, for_seconds = ?, updated_at = ? WHERE id = ?`,
			name, in.Enabled, in.Severity, string(params), in.ForSeconds, store.Ms(now), id); err != nil {
			return err
		}
		if !in.Enabled {
			for _, inst := range cur.Instances {
				if inst.State == "firing" {
					if err := insertHistory(ctx, tx, now, cur.ID, name, inst.Key, "resolved", cur.Severity, "rule disabled", inst.Value); err != nil {
						return err
					}
				}
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM alert_state WHERE rule_id = ?`, id); err != nil {
				return err
			}
		}
		return audit.Insert(ctx, tx, actor, "alert.rule.update", id, audit.OK, map[string]any{"enabled": in.Enabled, "severity": in.Severity})
	}); err != nil {
		return nil, err
	}
	return e.Get(ctx, id)
}

// Delete removes a custom rule and its state.
func (e *Engine) Delete(ctx context.Context, id string, actor audit.Actor) error {
	cur, err := e.Get(ctx, id)
	if err != nil {
		return err
	}
	if cur.Builtin {
		return ErrBuiltin
	}
	return e.db.Write(ctx, func(tx *sql.Tx) error {
		for _, q := range []string{`DELETE FROM alert_state WHERE rule_id = ?`, `DELETE FROM alert_rules WHERE id = ?`} {
			if _, err := tx.ExecContext(ctx, q, id); err != nil {
				return err
			}
		}
		return audit.Insert(ctx, tx, actor, "alert.rule.delete", id, audit.OK, map[string]any{"name": cur.Name})
	})
}

// HistoryQuery filters History.
type HistoryQuery struct {
	RuleID   string
	BeforeID int64
	Limit    int
}

// History returns transitions, newest first.
func (e *Engine) History(ctx context.Context, q HistoryQuery) ([]AlertHistoryEntry, error) {
	if q.Limit <= 0 || q.Limit > 500 {
		q.Limit = 100
	}
	where, args := []string{"1=1"}, []any{}
	if q.RuleID != "" {
		where, args = append(where, "rule_id = ?"), append(args, q.RuleID)
	}
	if q.BeforeID > 0 {
		where, args = append(where, "id < ?"), append(args, q.BeforeID)
	}
	rows, err := e.db.R().QueryContext(ctx, `SELECT id, ts, rule_id, rule_name, key, state, severity, summary, value FROM alert_history WHERE `+
		strings.Join(where, " AND ")+` ORDER BY id DESC LIMIT ?`, append(args, q.Limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AlertHistoryEntry{}
	for rows.Next() {
		var h AlertHistoryEntry
		var ts int64
		if err := rows.Scan(&h.ID, &ts, &h.RuleID, &h.RuleName, &h.Key, &h.State, &h.Severity, &h.Summary, &h.Value); err != nil {
			return nil, err
		}
		h.Time = store.FromMs(ts).UTC()
		out = append(out, h)
	}
	return out, rows.Err()
}

// Prune drops history older than HistoryRetention (PL-DB-005).
func (e *Engine) Prune(ctx context.Context) error {
	cut := store.Ms(e.o.Now().Add(-HistoryRetention))
	return e.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM alert_history WHERE ts < ?`, cut)
		return err
	})
}

func insertHistory(ctx context.Context, tx *sql.Tx, at time.Time, ruleID, name, key, state, severity, summary string, value float64) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO alert_history(ts, rule_id, rule_name, key, state, severity, summary, value) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		store.Ms(at), ruleID, name, key, state, severity, summary, value)
	return err
}

// Run evaluates every Interval until ctx ends.
func (e *Engine) Run(ctx context.Context) {
	for _, n := range e.o.Notifiers {
		go n.Run(ctx)
	}
	t := time.NewTicker(e.o.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.Evaluate(ctx)
		}
	}
}

// transition is a state change to publish and notify after the transaction commits.
type transition struct {
	rule  *AlertRule
	inst  AlertInstance
	state string // firing | resolved | reminder
	at    time.Time
}

// Evaluate runs every enabled rule once and applies transitions.
func (e *Engine) Evaluate(ctx context.Context) {
	e.run.Lock()
	defer e.run.Unlock()
	rules, err := e.List(ctx)
	if err != nil {
		e.o.Log.Warn("alerts: load rules", "err", err)
		return
	}
	now := e.o.Now()
	var out []transition
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		findings, noData, err := e.eval(ctx, r, now)
		info := evalInfo{at: now.UTC(), noData: noData}
		if err != nil {
			info.err = err.Error()
			e.o.Log.Warn("alerts: evaluate", "rule", r.ID, "err", err)
		}
		e.mu.Lock()
		e.evals[r.ID] = info
		e.mu.Unlock()
		if err != nil || noData != "" {
			continue // keep the current state: no data is not "resolved"
		}
		ts, err := e.apply(ctx, r, findings, now)
		if err != nil {
			e.o.Log.Warn("alerts: apply", "rule", r.ID, "err", err)
			continue
		}
		out = append(out, ts...)
	}
	for _, t := range out {
		e.emit(t)
	}
}

func (e *Engine) apply(ctx context.Context, r *AlertRule, findings []Finding, now time.Time) ([]transition, error) {
	var out []transition
	cur := map[string]AlertInstance{}
	for _, in := range r.Instances {
		cur[in.Key] = in
	}
	seen := map[string]bool{}
	hold := time.Duration(r.ForSeconds) * time.Second
	err := e.db.Write(ctx, func(tx *sql.Tx) error {
		out = out[:0]
		for _, f := range findings {
			if seen[f.Key] {
				continue
			}
			seen[f.Key] = true
			in, exists := cur[f.Key]
			if !exists {
				in = AlertInstance{Key: f.Key, State: "pending", Since: now}
			}
			in.Value, in.Summary = f.Value, f.Summary
			var lastNotified int64
			if in.State == "pending" && now.Sub(in.Since) >= hold {
				in.State, in.FiredAt = "firing", now
				lastNotified = store.Ms(now)
				if err := insertHistory(ctx, tx, now, r.ID, r.Name, f.Key, "firing", r.Severity, f.Summary, f.Value); err != nil {
					return err
				}
				out = append(out, transition{rule: r, inst: in, state: "firing", at: now})
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO alert_state(rule_id, key, state, since, fired_at, last_notified, value, summary)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT(rule_id, key) DO UPDATE SET state = excluded.state, fired_at = CASE WHEN excluded.fired_at > 0 THEN excluded.fired_at ELSE fired_at END,
				  last_notified = CASE WHEN excluded.last_notified > 0 THEN excluded.last_notified ELSE last_notified END,
				  value = excluded.value, summary = excluded.summary`,
				r.ID, f.Key, in.State, store.Ms(in.Since), msOrZero(in.FiredAt), lastNotified, f.Value, f.Summary); err != nil {
				return err
			}
			if exists && in.State == "firing" && e.o.Repeat > 0 {
				var ln int64
				if err := tx.QueryRowContext(ctx, `SELECT last_notified FROM alert_state WHERE rule_id = ? AND key = ?`, r.ID, f.Key).Scan(&ln); err != nil {
					return err
				}
				if ln > 0 && now.Sub(store.FromMs(ln)) >= e.o.Repeat {
					if _, err := tx.ExecContext(ctx, `UPDATE alert_state SET last_notified = ? WHERE rule_id = ? AND key = ?`, store.Ms(now), r.ID, f.Key); err != nil {
						return err
					}
					out = append(out, transition{rule: r, inst: in, state: "reminder", at: now})
				}
			}
		}
		for key, in := range cur {
			if seen[key] {
				continue
			}
			if in.State == "firing" {
				summary := "resolved: " + in.Summary
				if err := insertHistory(ctx, tx, now, r.ID, r.Name, key, "resolved", r.Severity, summary, in.Value); err != nil {
					return err
				}
				in.Summary = summary
				out = append(out, transition{rule: r, inst: in, state: "resolved", at: now})
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM alert_state WHERE rule_id = ? AND key = ?`, r.ID, key); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

func msOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return store.Ms(t)
}

// Event is the payload of `_sys.alert.<rule>` events.
type Event struct {
	RuleID   string    `json:"rule_id"`
	RuleName string    `json:"rule_name"`
	Kind     string    `json:"kind"`
	Key      string    `json:"key"`
	State    string    `json:"state"`
	Severity string    `json:"severity"`
	Summary  string    `json:"summary"`
	Value    float64   `json:"value"`
	Since    time.Time `json:"since"`
	At       time.Time `json:"at"`
}

// Alert event types.
const (
	TypeFiring   = "pulse.alert.firing.v1"
	TypeResolved = "pulse.alert.resolved.v1"
)

func (e *Engine) emit(t transition) {
	if t.state != "reminder" {
		metrics.AlertTransitions.WithLabelValues(t.rule.ID, t.state).Inc()
		e.o.Log.Info("alert "+t.state, "rule", t.rule.ID, "key", t.inst.Key, "severity", t.rule.Severity, "summary", t.inst.Summary)
		e.publish(t)
	}
	for _, n := range e.o.Notifiers {
		n.Enqueue(notify.Message{Title: t.rule.Name, Text: t.inst.Summary, Severity: t.rule.Severity, State: t.state, Key: t.inst.Key, At: t.at})
	}
}

func (e *Engine) publish(t transition) {
	if e.o.Publish == nil {
		return
	}
	ev := Event{RuleID: t.rule.ID, RuleName: t.rule.Name, Kind: t.rule.Kind, Key: t.inst.Key, State: t.state, Severity: t.rule.Severity,
		Summary: t.inst.Summary, Value: t.inst.Value, Since: t.inst.Since.UTC(), At: t.at.UTC()}
	data, _ := json.Marshal(ev)
	typ := TypeFiring
	if t.state == "resolved" {
		typ = TypeResolved
	}
	m := nats.NewMsg(SubjectPrefix + t.rule.ID)
	m.Header.Set(envelope.HeaderID, envelope.NewID())
	m.Header.Set(envelope.HeaderType, typ)
	m.Header.Set(envelope.HeaderSource, "pulsed")
	m.Header.Set(envelope.HeaderTime, t.at.UTC().Format(envelope.TimeFormat))
	m.Header.Set(envelope.HeaderContentType, "application/json")
	m.Data = data
	if err := e.o.Publish.PublishMsg(m); err != nil {
		e.o.Log.Warn("alerts: publish event", "rule", t.rule.ID, "err", err)
	}
}
