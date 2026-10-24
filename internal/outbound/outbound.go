// Package outbound delivers events to HTTP endpoints (PL-HOOK-005..010): one durable consumer per hook, in order,
// with exponential backoff, HMAC signatures, an SSRF-safe client and a delivery log in SQLite.
package outbound

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/oklog/ulid/v2"

	"github.com/aurimasniekis/go-pulse/internal/audit"
	"github.com/aurimasniekis/go-pulse/internal/metrics"
	"github.com/aurimasniekis/go-pulse/internal/netguard"
	"github.com/aurimasniekis/go-pulse/internal/secretbox"
	"github.com/aurimasniekis/go-pulse/internal/store"
	"github.com/aurimasniekis/go-pulse/pkg/pulse/envelope"
)

// Errors.
var (
	ErrNotFound = errors.New("outbound hook not found")
	ErrInvalid  = errors.New("invalid outbound hook")
)

// DefaultBackoff holds the delays between attempts: 12 attempts, waiting 1 s … 1 h between them (PL-HOOK-006).
var DefaultBackoff = []time.Duration{time.Second, 2 * time.Second, 5 * time.Second, 15 * time.Second, 30 * time.Second,
	time.Minute, 5 * time.Minute, 10 * time.Minute, 20 * time.Minute, 40 * time.Minute, time.Hour}

const (
	stream          = "EVENTS"
	excerptLimit    = 2048
	deliveryTimeout = 10 * time.Second
	// DeliveryRetention is how long the delivery log is kept (PL-HOOK-009).
	DeliveryRetention = 30 * 24 * time.Hour
)

// Hook is an outbound webhook (never includes the secret).
type Hook struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Filter    string            `json:"filter"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers"`
	Paused    bool              `json:"paused"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
	Consumer  string            `json:"consumer"`
	Pending   uint64            `json:"pending" doc:"Events matching the filter not yet delivered"`
	Last      *Delivery         `json:"last,omitempty"`
}

// Delivery is one attempt.
type Delivery struct {
	ID              int64     `json:"id"`
	HookID          string    `json:"hook_id"`
	Time            time.Time `json:"time"`
	Seq             uint64    `json:"seq"`
	Subject         string    `json:"subject"`
	EventID         string    `json:"event_id"`
	Attempt         int       `json:"attempt"`
	Outcome         string    `json:"outcome" enum:"delivered,retry,dead,manual,test"`
	StatusCode      int       `json:"status_code"`
	Error           string    `json:"error,omitempty"`
	LatencyMs       int64     `json:"latency_ms"`
	ResponseExcerpt string    `json:"response_excerpt,omitempty"`
}

// Options configure the service.
type Options struct {
	Backoff []time.Duration
	Allow   []string // networks exempt from the SSRF guard (PULSE_OUTBOUND_ALLOW)
	Version string
	Log     *slog.Logger
}

// Service manages hooks and their dispatchers.
type Service struct {
	db      *store.DB
	js      jetstream.JetStream
	master  []byte
	guard   *netguard.Guard
	client  *http.Client
	backoff []time.Duration
	ua      string
	log     *slog.Logger
	now     func() time.Time

	mu      sync.Mutex
	runners map[string]*runner
	ctx     context.Context
	wg      sync.WaitGroup
}

// New returns a service; call Start to run dispatchers.
func New(db *store.DB, js jetstream.JetStream, master []byte, o Options) (*Service, error) {
	g, err := netguard.New(o.Allow)
	if err != nil {
		return nil, err
	}
	if len(o.Backoff) == 0 {
		o.Backoff = DefaultBackoff
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return &Service{db: db, js: js, master: master, guard: g, client: g.Client(deliveryTimeout), backoff: o.Backoff,
		ua: "pulse-webhooks/" + o.Version, log: o.Log.With("component", "outbound"), now: store.Now, runners: map[string]*runner{}}, nil
}

// MaxAttempts is the number of delivery attempts before a dead letter (one more than the delays between them).
func (s *Service) MaxAttempts() int { return len(s.backoff) + 1 }

var (
	nameRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,47}$`)
	headerRe = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
)

// Input creates or updates an outbound hook.
type Input struct {
	Name    string
	Filter  string
	URL     string
	Secret  string // empty generates one on create, keeps the current one on update
	Headers map[string]string
}

func (s *Service) validate(in *Input) error {
	if !nameRe.MatchString(in.Name) {
		return fmt.Errorf("%w: name must match %s", ErrInvalid, nameRe)
	}
	if err := envelope.ValidateFilter(in.Filter); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if err := s.guard.Validate(in.URL); err != nil {
		return fmt.Errorf("%w: url: %w", ErrInvalid, err)
	}
	for k := range in.Headers {
		lk := strings.ToLower(k)
		if !headerRe.MatchString(k) || strings.HasPrefix(lk, "pulse-") || lk == "host" || lk == "content-length" || lk == "content-type" {
			return fmt.Errorf("%w: header %q cannot be set", ErrInvalid, k)
		}
	}
	if in.Secret != "" && len(in.Secret) < 16 {
		return fmt.Errorf("%w: secrets must be at least 16 characters", ErrInvalid)
	}
	return nil
}

func consumerName(id string) string { return "pulse_out_" + id }

// Create stores a hook and starts delivering events published from now on. Returns the signing secret (shown once).
func (s *Service) Create(ctx context.Context, in Input, actor audit.Actor) (*Hook, string, error) {
	if err := s.validate(&in); err != nil {
		return nil, "", err
	}
	if in.Secret == "" {
		b := make([]byte, 32)
		_, _ = rand.Read(b)
		in.Secret = "whsec_" + hex.EncodeToString(b)
	}
	enc, err := secretbox.Seal(s.master, []byte(in.Secret))
	if err != nil {
		return nil, "", err
	}
	hdr, _ := json.Marshal(nonNil(in.Headers))
	id := strings.ToLower(ulid.MustNew(ulid.Now(), rand.Reader).String())
	now := s.now()
	// The consumer exists before the row, so a crash in between leaves at most an unused consumer.
	if _, err := s.js.CreateOrUpdateConsumer(ctx, stream, consumerConfig(id, in.Name, in.Filter, s.backoff)); err != nil {
		return nil, "", fmt.Errorf("create consumer: %w", err)
	}
	err = s.db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO hooks_out(id, name, filter, url, secret_enc, headers, paused, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?)`, id, in.Name, in.Filter, in.URL, enc, string(hdr), store.Ms(now), store.Ms(now)); err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				return fmt.Errorf("%w: a hook named %q exists", ErrInvalid, in.Name)
			}
			return err
		}
		return audit.Insert(ctx, tx, actor, "hook_out.create", id, audit.OK, map[string]any{"name": in.Name, "filter": in.Filter, "url": in.URL})
	})
	if err != nil {
		_ = s.js.DeleteConsumer(ctx, stream, consumerName(id))
		return nil, "", err
	}
	s.reconcile(id) //nolint:contextcheck // dispatchers run on the service context, not the request's
	h, err := s.Get(ctx, id)
	return h, in.Secret, err
}

func consumerConfig(id, name, filter string, backoff []time.Duration) jetstream.ConsumerConfig {
	return jetstream.ConsumerConfig{
		Durable:       consumerName(id),
		Description:   "pulsed: outbound hook " + name,
		FilterSubject: filter,
		DeliverPolicy: jetstream.DeliverNewPolicy,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       deliveryTimeout + 20*time.Second,
		MaxDeliver:    len(backoff) + 2, // one spare: pulsed terms the message itself after the last attempt
		MaxAckPending: 1,                // strictly one in flight: deliveries keep stream order
	}
}

// Update changes URL, headers, filter or secret. Changing the filter keeps the consumer's position.
func (s *Service) Update(ctx context.Context, id string, in Input, actor audit.Actor) (*Hook, error) {
	if err := s.validate(&in); err != nil {
		return nil, err
	}
	cur, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if cur.Filter != in.Filter {
		cfg := consumerConfig(id, in.Name, in.Filter, s.backoff)
		if _, err := s.js.UpdateConsumer(ctx, stream, cfg); err != nil {
			return nil, fmt.Errorf("update consumer: %w", err)
		}
	}
	var enc []byte
	if in.Secret != "" {
		if enc, err = secretbox.Seal(s.master, []byte(in.Secret)); err != nil {
			return nil, err
		}
	}
	hdr, _ := json.Marshal(nonNil(in.Headers))
	err = s.db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE hooks_out SET name = ?, filter = ?, url = ?, headers = ?, updated_at = ?,
			secret_enc = COALESCE(?, secret_enc) WHERE id = ?`, in.Name, in.Filter, in.URL, string(hdr), store.Ms(s.now()), enc, id); err != nil {
			return err
		}
		return audit.Insert(ctx, tx, actor, "hook_out.update", id, audit.OK, map[string]any{"name": in.Name, "url": in.URL, "secret_rotated": in.Secret != ""})
	})
	if err != nil {
		return nil, err
	}
	s.restart(id) //nolint:contextcheck // dispatchers run on the service context, not the request's
	return s.Get(ctx, id)
}

// SetPaused pauses or resumes delivery; the consumer keeps its position (PL-HOOK-010).
func (s *Service) SetPaused(ctx context.Context, id string, paused bool, actor audit.Actor) (*Hook, error) {
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "UPDATE hooks_out SET paused = ?, updated_at = ? WHERE id = ?", boolInt(paused), store.Ms(s.now()), id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		action := "hook_out.resume"
		if paused {
			action = "hook_out.pause"
		}
		return audit.Insert(ctx, tx, actor, action, id, audit.OK, nil)
	})
	if err != nil {
		return nil, err
	}
	s.reconcile(id) //nolint:contextcheck // dispatchers run on the service context, not the request's
	return s.Get(ctx, id)
}

// Delete stops delivery and removes the hook, its consumer and its delivery log.
func (s *Service) Delete(ctx context.Context, id string, actor audit.Actor) error {
	s.stop(id)
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "DELETE FROM hooks_out WHERE id = ?", id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM deliveries WHERE hook_id = ?", id); err != nil {
			return err
		}
		return audit.Insert(ctx, tx, actor, "hook_out.delete", id, audit.OK, nil)
	})
	if err != nil {
		return err
	}
	if err := s.js.DeleteConsumer(ctx, stream, consumerName(id)); err != nil && !errors.Is(err, jetstream.ErrConsumerNotFound) {
		s.log.Warn("delete consumer", "hook", id, "err", err)
	}
	return nil
}

type row struct {
	Hook
	secret []byte
}

func (s *Service) load(ctx context.Context, id string) (*row, error) {
	var r row
	var hdr string
	var paused int
	var c, u int64
	var enc []byte
	err := s.db.R().QueryRowContext(ctx, `SELECT id, name, filter, url, secret_enc, headers, paused, created_at, updated_at FROM hooks_out WHERE id = ?`, id).
		Scan(&r.ID, &r.Name, &r.Filter, &r.URL, &enc, &hdr, &paused, &c, &u)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(hdr), &r.Headers)
	r.Paused, r.CreatedAt, r.UpdatedAt, r.Consumer = paused == 1, store.FromMs(c), store.FromMs(u), consumerName(r.ID)
	if r.secret, err = secretbox.Open(s.master, enc); err != nil {
		return nil, err
	}
	return &r, nil
}

// Get returns one hook with its pending count and last delivery.
func (s *Service) Get(ctx context.Context, id string) (*Hook, error) {
	r, err := s.load(ctx, id)
	if err != nil {
		return nil, err
	}
	h := r.Hook
	if h.Headers == nil {
		h.Headers = map[string]string{}
	}
	if ci, err := s.js.Consumer(ctx, stream, h.Consumer); err == nil {
		h.Pending = ci.CachedInfo().NumPending + uint64(ci.CachedInfo().NumAckPending) //nolint:gosec // counts are non-negative
	}
	if ds, err := s.Deliveries(ctx, id, 0, 1); err == nil && len(ds) == 1 {
		h.Last = &ds[0]
	}
	return &h, nil
}

func (s *Service) ids(ctx context.Context) ([]string, error) {
	rows, err := s.db.R().QueryContext(ctx, "SELECT id FROM hooks_out ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// List returns all hooks.
func (s *Service) List(ctx context.Context) ([]*Hook, error) {
	ids, err := s.ids(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*Hook, 0, len(ids))
	for _, id := range ids {
		h, err := s.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, nil
}

// Deliveries returns the delivery log newest first.
func (s *Service) Deliveries(ctx context.Context, hookID string, beforeID int64, limit int) ([]Delivery, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT id, hook_id, ts, seq, subject, event_id, attempt, outcome, status_code, error, latency_ms, response_excerpt
		FROM deliveries WHERE hook_id = ?`
	args := []any{hookID}
	if beforeID > 0 {
		q += " AND id < ?"
		args = append(args, beforeID)
	}
	q += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.R().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Delivery{}
	for rows.Next() {
		var d Delivery
		var ts int64
		if err := rows.Scan(&d.ID, &d.HookID, &ts, &d.Seq, &d.Subject, &d.EventID, &d.Attempt, &d.Outcome, &d.StatusCode, &d.Error,
			&d.LatencyMs, &d.ResponseExcerpt); err != nil {
			return nil, err
		}
		d.Time = store.FromMs(ts)
		out = append(out, d)
	}
	return out, rows.Err()
}

// PruneDeliveries drops log entries older than DeliveryRetention.
func (s *Service) PruneDeliveries(ctx context.Context) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM deliveries WHERE ts < ?", store.Ms(s.now().Add(-DeliveryRetention)))
		return err
	})
}

func (s *Service) record(ctx context.Context, d Delivery) {
	metrics.OutboundDeliveries.WithLabelValues(d.Outcome).Inc()
	if len(d.ResponseExcerpt) > excerptLimit {
		d.ResponseExcerpt = d.ResponseExcerpt[:excerptLimit]
	}
	if err := s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO deliveries(hook_id, ts, seq, subject, event_id, attempt, outcome, status_code, error, latency_ms, response_excerpt)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, d.HookID, store.Ms(s.now()), d.Seq, d.Subject, d.EventID, d.Attempt, d.Outcome, d.StatusCode,
			d.Error, d.LatencyMs, d.ResponseExcerpt)
		return err
	}); err != nil {
		s.log.Warn("record delivery", "hook", d.HookID, "err", err)
	}
}

// Sign returns the Pulse-Signature header value: t=<unix>,v1=<hex HMAC-SHA256(secret, "<t>.<body>")> (PL-HOOK-007).
func Sign(secret, body []byte, t time.Time) string {
	ts := strconv.FormatInt(t.Unix(), 10)
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(ts + "."))
	m.Write(body)
	return "t=" + ts + ",v1=" + hex.EncodeToString(m.Sum(nil))
}

// attempt performs one HTTP delivery and returns the delivery record (Outcome unset).
func (s *Service) attempt(ctx context.Context, r *row, e envelope.Event, attempt int) Delivery {
	d := Delivery{HookID: r.ID, Seq: e.Sequence, Subject: e.Subject, EventID: e.ID, Attempt: attempt}
	ct := e.ContentType
	if ct == "" {
		ct = envelope.ContentBinary
	}
	ctx, cancel := context.WithTimeout(ctx, deliveryTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.URL, bytes.NewReader(e.Data))
	if err != nil {
		d.Error = cleanErr(err)
		return d
	}
	for k, v := range r.Headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", ct)
	req.Header.Set("User-Agent", s.ua)
	req.Header.Set("Pulse-Signature", Sign(r.secret, e.Data, s.now()))
	req.Header.Set("Pulse-Subject", e.Subject)
	req.Header.Set("Pulse-Stream-Seq", strconv.FormatUint(e.Sequence, 10))
	req.Header.Set("Pulse-Delivery-Attempt", strconv.Itoa(attempt))
	for k, v := range map[string]string{"Pulse-Event-Id": e.ID, "Pulse-Event-Type": e.Type, "Pulse-Event-Source": e.Source} {
		if v != "" {
			req.Header.Set(k, v)
		}
	}
	if !e.Time.IsZero() {
		req.Header.Set("Pulse-Event-Time", e.Time.UTC().Format(time.RFC3339Nano))
	}
	start := time.Now()
	resp, err := s.client.Do(req)
	d.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		d.Error = cleanErr(err)
		return d
	}
	defer resp.Body.Close()
	ex, _ := io.ReadAll(io.LimitReader(resp.Body, excerptLimit))
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	d.StatusCode, d.ResponseExcerpt = resp.StatusCode, string(ex)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		d.Error = "HTTP " + resp.Status
	}
	return d
}

// cleanErr makes a transport error safe to store and log. *url.Error embeds the full URL, and destination URLs often carry
// the secret (Slack and Discord webhook paths, tokens in query strings), so only the operation and the cause are kept
// (PL-OBS-001).
func cleanErr(err error) string {
	if errors.Is(err, netguard.ErrBlocked) {
		return "blocked: destination resolves to an internal address (allowlist it with PULSE_OUTBOUND_ALLOW)"
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = fmt.Errorf("%s: %w", ue.Op, ue.Err)
	}
	msg := err.Error()
	if len(msg) > 300 {
		msg = msg[:300]
	}
	return msg
}

func ok(d Delivery) bool { return d.Error == "" && d.StatusCode >= 200 && d.StatusCode <= 299 }

// Retry delivers one logged event again, once, outside the consumer (PL-HOOK-009). It does not change the
// consumer's position.
func (s *Service) Retry(ctx context.Context, deliveryID int64, actor audit.Actor) (*Delivery, error) {
	var hookID string
	var seq uint64
	if err := s.db.R().QueryRowContext(ctx, "SELECT hook_id, seq FROM deliveries WHERE id = ?", deliveryID).Scan(&hookID, &seq); err != nil {
		return nil, ErrNotFound
	}
	r, err := s.load(ctx, hookID)
	if err != nil {
		return nil, err
	}
	st, err := s.js.Stream(ctx, stream)
	if err != nil {
		return nil, err
	}
	m, err := st.GetMsg(ctx, seq)
	if err != nil {
		return nil, fmt.Errorf("event %d is no longer stored: %w", seq, err)
	}
	e := envelope.Decode(m.Subject, m.Header, m.Data)
	e.Sequence, e.Stream = seq, stream
	d := s.attempt(ctx, r, e, 1)
	d.Outcome = "manual"
	s.record(ctx, d)
	_ = audit.Write(ctx, s.db, actor, "hook_out.retry", hookID, audit.OK, map[string]any{"seq": seq, "status": d.StatusCode})
	return &d, nil
}

// Test sends a synthetic event (not stored) to the hook's URL.
func (s *Service) Test(ctx context.Context, id string, actor audit.Actor) (*Delivery, error) {
	r, err := s.load(ctx, id)
	if err != nil {
		return nil, err
	}
	body, _ := json.Marshal(map[string]any{"test": true, "hook": r.Name, "time": s.now()})
	e := envelope.Event{Subject: "evt.pulse.webhook.test", ID: envelope.NewID(), Type: "pulse.webhook.test.v1", Source: "pulsed",
		Time: s.now(), ContentType: envelope.ContentJSON, Data: body}
	d := s.attempt(ctx, r, e, 1)
	d.Outcome = "test"
	s.record(ctx, d)
	_ = audit.Write(ctx, s.db, actor, "hook_out.test", id, audit.OK, map[string]any{"status": d.StatusCode})
	return &d, nil
}

func nonNil(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
