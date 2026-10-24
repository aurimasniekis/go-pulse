package rollup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/aurimasniekis/go-pulse/internal/metrics"
	"github.com/aurimasniekis/go-pulse/internal/store"
)

// Defaults.
const (
	DefaultFlushEvery  = 10 * time.Second
	DefaultSubjectCap  = 50_000
	MinuteRetention    = 8 * 24 * time.Hour
	HourRetention      = 400 * 24 * time.Hour
	cacheWindow        = 48 * time.Hour
	keySampleInterval  = 10 * time.Minute
	keySampleBatch     = 50
	maxPendingMessages = 20_000
)

// Options configure the engine.
type Options struct {
	FlushEvery time.Duration
	SubjectCap int
	// Streams limits which streams are counted; empty means every plain stream (not KV_ or OBJ_).
	Streams []string
	Log     *slog.Logger
	Now     func() time.Time
}

// Engine runs one worker per stream.
type Engine struct {
	js  jetstream.JetStream
	db  *store.DB
	o   Options
	log *slog.Logger

	mu      sync.Mutex
	workers map[string]*worker
	cache   *minuteCache
	wg      sync.WaitGroup
}

// New returns an engine.
func New(js jetstream.JetStream, db *store.DB, o Options) *Engine {
	if o.FlushEvery <= 0 {
		o.FlushEvery = DefaultFlushEvery
	}
	if o.SubjectCap <= 0 {
		o.SubjectCap = DefaultSubjectCap
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Engine{js: js, db: db, o: o, log: o.Log.With("component", "rollup"), workers: map[string]*worker{}, cache: newMinuteCache()}
}

// Cache exposes the in-memory minute totals (last 48 h).
func (e *Engine) Cache() *minuteCache { return e.cache }

func (e *Engine) wanted(name string) bool {
	if len(e.o.Streams) > 0 {
		for _, s := range e.o.Streams {
			if s == name {
				return true
			}
		}
		return false
	}
	return !strings.HasPrefix(name, "KV_") && !strings.HasPrefix(name, "OBJ_")
}

// Run discovers streams every minute and keeps one worker per stream until ctx ends; workers flush on exit.
func (e *Engine) Run(ctx context.Context) {
	if err := e.loadCache(ctx); err != nil {
		e.log.Warn("load minute cache", "err", err)
	}
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	prune := time.NewTicker(time.Hour)
	defer prune.Stop()
	sample := time.NewTicker(time.Minute)
	defer sample.Stop()
	e.sync(ctx)
	for {
		select {
		case <-ctx.Done():
			e.stopAll()
			return
		case <-t.C:
			e.sync(ctx)
		case <-prune.C:
			if err := e.Prune(ctx); err != nil {
				e.log.Warn("prune rollups", "err", err)
			}
		case <-sample.C:
			if err := e.SampleKeys(ctx); err != nil {
				e.log.Warn("sample subject keys", "err", err)
			}
		}
	}
}

func (e *Engine) sync(ctx context.Context) {
	names := map[string]bool{}
	l := e.js.StreamNames(ctx)
	for n := range l.Name() {
		if e.wanted(n) {
			names[n] = true
		}
	}
	if err := l.Err(); err != nil {
		e.log.Warn("list streams", "err", err)
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for n := range names {
		if _, ok := e.workers[n]; !ok {
			w := &worker{e: e, stream: n}
			if err := w.start(ctx); err != nil {
				e.log.Warn("start rollup worker", "stream", n, "err", err)
				continue
			}
			e.workers[n] = w
		}
	}
	for n, w := range e.workers {
		if !names[n] {
			w.stop()
			delete(e.workers, n)
		}
	}
}

func (e *Engine) stopAll() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for n, w := range e.workers {
		w.stop()
		delete(e.workers, n)
	}
	e.wg.Wait()
}

// FlushAll forces every worker to flush now (tests, shutdown).
func (e *Engine) FlushAll(ctx context.Context) error {
	e.mu.Lock()
	ws := make([]*worker, 0, len(e.workers))
	for _, w := range e.workers {
		ws = append(ws, w)
	}
	e.mu.Unlock()
	var errs []error
	for _, w := range ws {
		errs = append(errs, w.flush(ctx))
	}
	return errors.Join(errs...)
}

// SyncNow discovers streams immediately (tests).
func (e *Engine) SyncNow(ctx context.Context) { e.sync(ctx) }

type counts struct{ msgs, bytes int64 }

type rowKey struct {
	dim, key string
	bucket   int64
}

type subjAgg struct {
	first, last int64
	lastSeq     uint64
	lastType    string
	msgs, bytes int64
}

// window is what accumulated since the last flush.
type window struct {
	rows     [3]map[rowKey]*counts // per resolution
	subjects map[string]*subjAgg
	lastSeq  uint64
	n        int
}

func newWindow() *window {
	w := &window{subjects: map[string]*subjAgg{}}
	for i := range w.rows {
		w.rows[i] = map[rowKey]*counts{}
	}
	return w
}

func (w *window) add(res int, k rowKey, size int64) {
	c := w.rows[res][k]
	if c == nil {
		c = &counts{}
		w.rows[res][k] = c
	}
	c.msgs++
	c.bytes += size
}

type worker struct {
	e       *Engine
	stream  string
	cc      jetstream.ConsumeContext
	mu      sync.Mutex
	win     *window
	flushMu sync.Mutex
	cancel  context.CancelFunc
	ctx     context.Context
	done    chan struct{}
}

func (w *worker) checkpoint(ctx context.Context) (uint64, error) {
	var seq int64
	err := w.e.db.R().QueryRowContext(ctx, "SELECT last_seq FROM rollup_checkpoints WHERE stream = ?", w.stream).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return uint64(seq), err //nolint:gosec // sequences are non-negative
}

func (w *worker) start(ctx context.Context) error {
	cp, err := w.checkpoint(ctx)
	if err != nil {
		return err
	}
	s, err := w.e.js.Stream(ctx, w.stream)
	if err != nil {
		return err
	}
	st := s.CachedInfo().State
	if cp > st.LastSeq {
		// The stream was purged or recreated: start over without double counting old buckets.
		w.e.log.Warn("rollup checkpoint is ahead of the stream; restarting from the first message", "stream", w.stream, "checkpoint", cp, "last_seq", st.LastSeq)
		cp = 0
	}
	cfg := jetstream.OrderedConsumerConfig{HeadersOnly: true, InactiveThreshold: time.Minute}
	if cp > 0 {
		cfg.DeliverPolicy, cfg.OptStartSeq = jetstream.DeliverByStartSequencePolicy, cp+1
	}
	cons, err := w.e.js.OrderedConsumer(ctx, w.stream, cfg)
	if err != nil {
		return err
	}
	w.win = newWindow()
	w.win.lastSeq = cp
	wctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	w.cancel, w.ctx = cancel, wctx
	w.done = make(chan struct{})
	w.cc, err = cons.Consume(w.handle)
	if err != nil {
		cancel()
		return err
	}
	w.e.wg.Add(1)
	go w.loop(wctx) //nolint:gosec // detached on purpose: the worker outlives the call that started it and stops via cancel
	return nil
}

func (w *worker) handle(m jetstream.Msg) {
	md, err := m.Metadata()
	if err != nil {
		return
	}
	h := m.Headers()
	size := int64(len(m.Subject()))
	if v := h.Get("Nats-Msg-Size"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			size += n
		}
	}
	for k, vs := range h {
		if k == "Nats-Msg-Size" {
			continue
		}
		size += int64(len(k))
		for _, v := range vs {
			size += int64(len(v))
		}
	}
	typ := h.Get("ce-type")
	if typ == "" {
		typ = Untyped
	}
	ts := md.Timestamp.UTC().Unix()
	subj := m.Subject()
	pfx := prefixes(subj, 4)

	w.mu.Lock()
	win := w.win
	for i, r := range All {
		b := ts - ts%r.Step
		win.add(i, rowKey{DimTotal, "", b}, size)
		win.add(i, rowKey{DimType, typ, b}, size)
		for d, p := range pfx {
			if d+1 > r.MaxDepth {
				break
			}
			win.add(i, rowKey{PrefixDim(d + 1), p, b}, size)
		}
	}
	sa := win.subjects[subj]
	if sa == nil {
		sa = &subjAgg{first: ts}
		win.subjects[subj] = sa
	}
	sa.last, sa.lastSeq, sa.lastType = ts, md.Sequence.Stream, typ
	sa.msgs++
	sa.bytes += size
	win.lastSeq = md.Sequence.Stream
	win.n++
	full := win.n >= maxPendingMessages
	w.mu.Unlock()
	w.e.cache.add(w.stream, ts-ts%60, size)
	metrics.RollupEvents.WithLabelValues(w.stream).Inc()
	metrics.RollupBytes.WithLabelValues(w.stream).Add(float64(size))
	if full {
		go func() { _ = w.flush(w.ctx) }()
	}
}

func (w *worker) loop(ctx context.Context) {
	defer w.e.wg.Done()
	defer close(w.done)
	t := time.NewTicker(w.e.o.FlushEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			if err := w.flush(context.Background()); err != nil { //nolint:contextcheck // final flush outlives ctx
				w.e.log.Warn("final rollup flush", "stream", w.stream, "err", err)
			}
			return
		case <-t.C:
			if err := w.flush(ctx); err != nil {
				w.e.log.Warn("rollup flush", "stream", w.stream, "err", err)
			}
		}
	}
}

func (w *worker) stop() {
	w.cc.Stop()
	w.cancel()
	<-w.done
}

// flush commits the window: rollup upserts, subject index and checkpoint in one transaction (exactly once).
// On failure the window is merged back so nothing is lost.
func (w *worker) flush(ctx context.Context) error {
	w.flushMu.Lock()
	defer w.flushMu.Unlock()
	w.mu.Lock()
	win := w.win
	if win.n == 0 {
		w.mu.Unlock()
		return nil
	}
	w.win = newWindow()
	w.win.lastSeq = win.lastSeq
	w.mu.Unlock()

	err := w.e.db.Write(ctx, func(tx *sql.Tx) error { return w.commit(ctx, tx, win) })
	if err != nil {
		w.mu.Lock()
		w.win = merge(win, w.win)
		w.mu.Unlock()
		return err
	}
	return nil
}

func merge(old, cur *window) *window {
	for i := range cur.rows {
		for k, c := range cur.rows[i] {
			if o := old.rows[i][k]; o != nil {
				o.msgs += c.msgs
				o.bytes += c.bytes
			} else {
				old.rows[i][k] = c
			}
		}
	}
	for s, c := range cur.subjects {
		if o := old.subjects[s]; o != nil {
			o.last, o.lastSeq, o.lastType = c.last, c.lastSeq, c.lastType
			o.msgs += c.msgs
			o.bytes += c.bytes
		} else {
			old.subjects[s] = c
		}
	}
	old.lastSeq = max(old.lastSeq, cur.lastSeq)
	old.n += cur.n
	return old
}

func (w *worker) commit(ctx context.Context, tx *sql.Tx, win *window) error {
	for i, r := range All {
		if err := w.upsertRows(ctx, tx, r, win.rows[i]); err != nil {
			return err
		}
	}
	var have int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM subjects WHERE stream = ?", w.stream).Scan(&have); err != nil {
		return err
	}
	upd, err := tx.PrepareContext(ctx, `UPDATE subjects SET last_seen = MAX(last_seen, ?), last_seq = MAX(last_seq, ?),
		last_type = CASE WHEN ? > last_seq THEN ? ELSE last_type END, msgs = msgs + ?, bytes = bytes + ?
		WHERE stream = ? AND subject = ?`)
	if err != nil {
		return err
	}
	defer upd.Close()
	ins, err := tx.PrepareContext(ctx, `INSERT INTO subjects(stream, subject, first_seen, last_seen, last_seq, last_type, msgs, bytes)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer ins.Close()
	// Deterministic order so the cap keeps the same subjects across runs.
	names := make([]string, 0, len(win.subjects))
	for s := range win.subjects {
		names = append(names, s)
	}
	sort.Strings(names)
	var dropped int64
	for _, s := range names {
		a := win.subjects[s]
		res, err := upd.ExecContext(ctx, a.last, a.lastSeq, a.lastSeq, a.lastType, a.msgs, a.bytes, w.stream, s)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			continue
		}
		if have >= w.e.o.SubjectCap {
			dropped++
			continue
		}
		if _, err := ins.ExecContext(ctx, w.stream, s, a.first, a.last, a.lastSeq, a.lastType, a.msgs, a.bytes); err != nil {
			return err
		}
		have++
	}
	if dropped > 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO subject_overflow(stream, dropped) VALUES (?, ?)
			ON CONFLICT(stream) DO UPDATE SET dropped = dropped + excluded.dropped`, w.stream, dropped); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO rollup_checkpoints(stream, last_seq, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(stream) DO UPDATE SET last_seq = excluded.last_seq, updated_at = excluded.updated_at`,
		w.stream, win.lastSeq, store.Ms(store.Now()))
	return err
}

func (w *worker) upsertRows(ctx context.Context, tx *sql.Tx, r Resolution, rows map[rowKey]*counts) error {
	stmt, err := tx.PrepareContext(ctx, fmt.Sprintf(`INSERT INTO %s(stream, dim, key, bucket, msgs, bytes) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(stream, dim, key, bucket) DO UPDATE SET msgs = msgs + excluded.msgs, bytes = bytes + excluded.bytes`, r.Table))
	if err != nil {
		return err
	}
	defer stmt.Close()
	for k, c := range rows {
		if _, err := stmt.ExecContext(ctx, w.stream, k.dim, k.key, k.bucket, c.msgs, c.bytes); err != nil {
			return err
		}
	}
	return nil
}

// Prune deletes minute buckets older than 8 days and hour buckets older than 400 days (PL-DB-005).
func (e *Engine) Prune(ctx context.Context) error {
	now := e.o.Now().UTC()
	return e.db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM rollup_1m WHERE bucket < ?", now.Add(-MinuteRetention).Unix()); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM rollup_1h WHERE bucket < ?", now.Add(-HourRetention).Unix())
		return err
	})
}

// SampleKeys refreshes json_keys for up to keySampleBatch subjects whose sample is older than 10 minutes, reading
// each subject's last message by direct get. Payload reads are bounded by the batch size.
func (e *Engine) SampleKeys(ctx context.Context) error {
	due, err := e.dueForSampling(ctx)
	if err != nil {
		return err
	}
	type result struct {
		item
		keys string
	}
	var out []result
	for _, it := range due {
		s, err := e.js.Stream(ctx, it.stream)
		if err != nil {
			continue
		}
		m, err := s.GetLastMsgForSubject(ctx, it.subject)
		keys := "[]"
		if err == nil {
			if k := JSONKeys(m.Data, 2); len(k) > 0 {
				b, _ := json.Marshal(k)
				keys = string(b)
			}
		}
		out = append(out, result{it, keys})
	}
	now := e.o.Now().Unix()
	return e.db.Write(ctx, func(tx *sql.Tx) error {
		for _, r := range out {
			if _, err := tx.ExecContext(ctx, "UPDATE subjects SET json_keys = ?, keys_sampled_at = ? WHERE stream = ? AND subject = ?",
				r.keys, now, r.stream, r.subject); err != nil {
				return err
			}
		}
		return nil
	})
}

type item struct{ stream, subject string }

func (e *Engine) dueForSampling(ctx context.Context) ([]item, error) {
	cutoff := e.o.Now().Add(-keySampleInterval).Unix()
	rows, err := e.db.R().QueryContext(ctx, `SELECT stream, subject FROM subjects WHERE keys_sampled_at < ? ORDER BY last_seen DESC LIMIT ?`,
		cutoff, keySampleBatch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var due []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.stream, &it.subject); err != nil {
			return nil, err
		}
		due = append(due, it)
	}
	return due, rows.Err()
}

// JSONKeys lists dotted key paths of a JSON object payload up to depth levels (arrays are not descended).
func JSONKeys(data []byte, depth int) []string {
	var v map[string]any
	if json.Unmarshal(data, &v) != nil {
		return nil
	}
	var out []string
	var walk func(prefix string, m map[string]any, d int)
	walk = func(prefix string, m map[string]any, d int) {
		for k, val := range m {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			out = append(out, p)
			if sub, ok := val.(map[string]any); ok && d < depth {
				walk(p, sub, d+1)
			}
		}
	}
	walk("", v, 1)
	sort.Strings(out)
	if len(out) > 100 {
		out = out[:100]
	}
	return out
}
