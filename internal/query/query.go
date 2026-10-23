// Package query is the explorer engine (PL-EXP-001..006, ADR-0022): paged reads over a stream with server-side
// filters, bounded by budgets.
package query

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/synadia-io/orbit.go/jetstreamext"
)

// Direction of a query.
type Direction string

// Directions.
const (
	Forward  Direction = "forward"
	Backward Direction = "backward"
)

// UntypedType matches events without ce-type in Request.Type.
const UntypedType = "(untyped)"

// Limits.
const (
	DefaultLimit = 50
	MaxLimit     = 500
	window       = 256
)

// Budget bounds one query (PL-EXP-006).
type Budget struct {
	MaxMsgs  int
	MaxBytes int64
	MaxTime  time.Duration
}

// DefaultBudget is 50k messages, 64 MB, 3 s.
var DefaultBudget = Budget{MaxMsgs: 50_000, MaxBytes: 64 << 20, MaxTime: 3 * time.Second}

// Request describes one page.
type Request struct {
	Stream    string
	Subjects  []string // filters; empty means everything in the stream
	Dir       Direction
	StartSeq  uint64    // forward: first sequence to read; backward: last sequence to read (inclusive)
	StartTime time.Time // forward only, alternative to StartSeq
	Limit     int
	Type      string            // exact ce-type, or UntypedType
	Headers   map[string]string // exact header values
	Contains  string            // payload substring
	JSONPath  string            // dot path into a JSON payload, e.g. "door.name"
	JSONValue string            // compared to the value at JSONPath rendered as a string
	Cursor    string
}

// Message is one returned event.
type Message struct {
	Stream  string              `json:"stream"`
	Seq     uint64              `json:"seq"`
	Subject string              `json:"subject"`
	Time    time.Time           `json:"time"`
	Headers map[string][]string `json:"headers"`
	Data    []byte              `json:"data"`
	Size    int                 `json:"size"`
}

// Result is a page.
type Result struct {
	Messages     []Message `json:"messages"`
	Next         string    `json:"next,omitempty"`
	Scanned      int       `json:"scanned"`
	ScannedBytes int64     `json:"scanned_bytes"`
	Partial      bool      `json:"partial"`
	Reason       string    `json:"reason,omitempty"` // msgs | bytes | time when Partial
	FirstSeq     uint64    `json:"first_seq"`
	LastSeq      uint64    `json:"last_seq"`
}

// Errors.
var (
	ErrBusy    = errors.New("too many concurrent queries")
	ErrRequest = errors.New("invalid query")
)

// Engine runs queries.
type Engine struct {
	js     jetstream.JetStream
	sem    chan struct{}
	budget Budget
}

// New returns an engine with at most concurrency parallel queries.
func New(js jetstream.JetStream, concurrency int, b Budget) *Engine {
	if concurrency <= 0 {
		concurrency = 4
	}
	return &Engine{js: js, sem: make(chan struct{}, concurrency), budget: b}
}

func (r *Request) normalize() error {
	if r.Stream == "" {
		return fmt.Errorf("%w: stream is required", ErrRequest)
	}
	if r.Dir == "" {
		r.Dir = Forward
	}
	if r.Dir != Forward && r.Dir != Backward {
		return fmt.Errorf("%w: direction must be forward or backward", ErrRequest)
	}
	if r.Limit <= 0 {
		r.Limit = DefaultLimit
	}
	if r.Limit > MaxLimit {
		r.Limit = MaxLimit
	}
	if !r.StartTime.IsZero() && (r.StartSeq != 0 || r.Dir == Backward) {
		return fmt.Errorf("%w: start time is forward-only and exclusive with start sequence", ErrRequest)
	}
	if (r.JSONPath == "") != (r.JSONValue == "") {
		return fmt.Errorf("%w: json path and value go together", ErrRequest)
	}
	for _, s := range r.Subjects {
		if s == "" || strings.ContainsAny(s, " \t") {
			return fmt.Errorf("%w: bad subject filter %q", ErrRequest, s)
		}
	}
	return nil
}

type run struct {
	e       *Engine
	r       *Request
	res     *Result
	started time.Time
	ctx     context.Context
}

func (x *run) exhausted() bool {
	switch {
	case x.res.Scanned >= x.e.budget.MaxMsgs:
		x.res.Partial, x.res.Reason = true, "msgs"
	case x.res.ScannedBytes >= x.e.budget.MaxBytes:
		x.res.Partial, x.res.Reason = true, "bytes"
	case time.Since(x.started) >= x.e.budget.MaxTime || x.ctx.Err() != nil:
		x.res.Partial, x.res.Reason = true, "time"
	default:
		return false
	}
	return true
}

// directGetHeaders are added by the server to direct-get replies; they describe the read, not the event.
var directGetHeaders = []string{"Nats-Stream", "Nats-Subject", "Nats-Sequence", "Nats-Time-Stamp", "Nats-Last-Sequence",
	"Nats-Num-Pending", "Nats-UpTo-Sequence"}

// StripReadHeaders removes direct-get metadata headers in place.
func StripReadHeaders(h nats.Header) {
	for _, k := range directGetHeaders {
		delete(h, k)
	}
}

func (x *run) consider(stream string, seq uint64, subject string, t time.Time, h nats.Header, data []byte) {
	StripReadHeaders(h)
	x.res.Scanned++
	size := len(subject) + len(data)
	for k, vs := range h {
		size += len(k)
		for _, v := range vs {
			size += len(v)
		}
	}
	x.res.ScannedBytes += int64(size)
	if !matchesAny(x.r.Subjects, subject) || !x.r.matches(h, data) {
		return
	}
	x.res.Messages = append(x.res.Messages, Message{Stream: stream, Seq: seq, Subject: subject, Time: t.UTC(),
		Headers: h, Data: data, Size: size})
}

func (r *Request) matches(h nats.Header, data []byte) bool {
	if r.Type != "" {
		t := h.Get("ce-type")
		if r.Type == UntypedType {
			if t != "" {
				return false
			}
		} else if t != r.Type {
			return false
		}
	}
	for k, v := range r.Headers {
		if h.Get(k) != v {
			return false
		}
	}
	if r.Contains != "" && !bytes.Contains(data, []byte(r.Contains)) {
		return false
	}
	if r.JSONPath != "" {
		v, ok := jsonLookup(data, r.JSONPath)
		if !ok || v != r.JSONValue {
			return false
		}
	}
	return true
}

// jsonLookup follows a dot path through objects (and numeric indexes into arrays) and renders scalars as strings.
func jsonLookup(data []byte, path string) (string, bool) {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return "", false
	}
	for _, p := range strings.Split(path, ".") {
		switch cur := v.(type) {
		case map[string]any:
			nv, ok := cur[p]
			if !ok {
				return "", false
			}
			v = nv
		case []any:
			var i int
			if _, err := fmt.Sscanf(p, "%d", &i); err != nil || i < 0 || i >= len(cur) {
				return "", false
			}
			v = cur[i]
		default:
			return "", false
		}
	}
	switch s := v.(type) {
	case string:
		return s, true
	case nil:
		return "null", true
	case map[string]any, []any:
		b, _ := json.Marshal(s)
		return string(b), true
	default:
		return fmt.Sprint(s), true
	}
}

// Run executes one page.
func (e *Engine) Run(ctx context.Context, r Request) (*Result, error) {
	if err := r.normalize(); err != nil {
		return nil, err
	}
	select {
	case e.sem <- struct{}{}:
		defer func() { <-e.sem }()
	case <-time.After(time.Second):
		return nil, ErrBusy
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	s, err := e.js.Stream(ctx, r.Stream)
	if err != nil {
		return nil, err
	}
	st := s.CachedInfo().State
	res := &Result{FirstSeq: st.FirstSeq, LastSeq: st.LastSeq, Messages: []Message{}}
	if r.Cursor != "" {
		c, err := decodeCursor(r.Cursor, &r)
		if err != nil {
			return nil, err
		}
		r.StartSeq, r.StartTime = c.Seq, time.Time{}
	}
	ctx, cancel := context.WithTimeout(ctx, e.budget.MaxTime+time.Second)
	defer cancel()
	x := &run{e: e, r: &r, res: res, started: time.Now(), ctx: ctx}
	if st.Msgs == 0 {
		return res, nil
	}
	var next uint64
	if r.Dir == Forward {
		next, err = x.forward(st)
	} else {
		next, err = x.backward(st)
	}
	if err != nil {
		return nil, err
	}
	if next != 0 {
		res.Next = encodeCursor(cursor{Stream: r.Stream, Dir: r.Dir, Seq: next, Hash: filterHash(&r)})
	}
	return res, nil
}

// forward returns the next sequence to read, or 0 when the end of the stream was reached.
func (x *run) forward(st jetstream.StreamState) (uint64, error) {
	if len(x.r.Subjects) > 1 {
		return x.forwardConsumer(st)
	}
	seq := x.r.StartSeq
	if seq == 0 && x.r.StartTime.IsZero() {
		seq = st.FirstSeq
	}
	if seq > st.LastSeq {
		return 0, nil
	}
	useTime := seq == 0
	for len(x.res.Messages) < x.r.Limit {
		if x.exhausted() {
			return seq, nil
		}
		opts := []jetstreamext.GetBatchOpt{}
		if useTime {
			opts = append(opts, jetstreamext.GetBatchStartTime(x.r.StartTime))
			useTime = false
		} else {
			opts = append(opts, jetstreamext.GetBatchSeq(seq))
		}
		if len(x.r.Subjects) == 1 {
			opts = append(opts, jetstreamext.GetBatchSubject(x.r.Subjects[0]))
		}
		batch := min(window, x.e.budget.MaxMsgs-x.res.Scanned)
		msgs, err := jetstreamext.GetBatch(x.ctx, x.e.js, x.r.Stream, batch, opts...)
		if err != nil {
			return 0, err
		}
		n, last, err := x.drain(msgs, func() bool { return len(x.res.Messages) >= x.r.Limit })
		if err != nil {
			return 0, err
		}
		if n == 0 {
			return 0, nil
		}
		seq = last + 1
		if len(x.res.Messages) >= x.r.Limit {
			if seq > st.LastSeq {
				return 0, nil
			}
			return seq, nil
		}
		if n < batch || last >= st.LastSeq {
			return 0, nil
		}
	}
	return seq, nil
}

// drain consumes a GetBatch iterator, stopping early when stop() is true. It returns how many messages were read and
// the last sequence read.
func (x *run) drain(msgs iter.Seq2[*jetstream.RawStreamMsg, error], stop func() bool) (int, uint64, error) {
	n := 0
	var last uint64
	for m, err := range msgs {
		if errors.Is(err, jetstreamext.ErrNoMessages) {
			break
		}
		if err != nil {
			return n, last, err
		}
		n++
		last = m.Sequence
		x.consider(x.r.Stream, m.Sequence, m.Subject, m.Time, m.Header, m.Data)
		if stop() {
			break
		}
	}
	return n, last, nil
}

func (x *run) forwardConsumer(st jetstream.StreamState) (uint64, error) {
	cfg := jetstream.OrderedConsumerConfig{FilterSubjects: x.r.Subjects, InactiveThreshold: 30 * time.Second}
	switch {
	case x.r.StartSeq > 0:
		cfg.DeliverPolicy, cfg.OptStartSeq = jetstream.DeliverByStartSequencePolicy, x.r.StartSeq
	case !x.r.StartTime.IsZero():
		t := x.r.StartTime
		cfg.DeliverPolicy, cfg.OptStartTime = jetstream.DeliverByStartTimePolicy, &t
	default:
		cfg.DeliverPolicy = jetstream.DeliverAllPolicy
	}
	cons, err := x.e.js.OrderedConsumer(x.ctx, x.r.Stream, cfg)
	if err != nil {
		return 0, err
	}
	var last uint64
	for len(x.res.Messages) < x.r.Limit && !x.exhausted() {
		batch, err := cons.Fetch(min(window, x.r.Limit-len(x.res.Messages)), jetstream.FetchMaxWait(500*time.Millisecond))
		if err != nil {
			return 0, err
		}
		got := 0
		var pending uint64 = 1
		for m := range batch.Messages() {
			got++
			md, err := m.Metadata()
			if err != nil {
				continue
			}
			last, pending = md.Sequence.Stream, md.NumPending
			x.consider(x.r.Stream, last, m.Subject(), md.Timestamp, m.Headers(), m.Data())
		}
		if batch.Error() != nil && !errors.Is(batch.Error(), nats.ErrTimeout) {
			return 0, batch.Error()
		}
		if got == 0 || pending == 0 {
			return 0, nil
		}
	}
	if last == 0 || last >= st.LastSeq {
		return 0, nil
	}
	return last + 1, nil
}

// backward walks windows of sequences from the top down and returns matches newest first.
func (x *run) backward(st jetstream.StreamState) (uint64, error) {
	hi := x.r.StartSeq
	if hi == 0 || hi > st.LastSeq {
		hi = st.LastSeq
	}
	for hi >= st.FirstSeq && len(x.res.Messages) < x.r.Limit {
		if x.exhausted() {
			return hi, nil
		}
		lo := st.FirstSeq
		if hi >= st.FirstSeq+window {
			lo = hi - window + 1
		}
		var page []Message
		saved := x.res.Messages
		x.res.Messages = nil
		opts := []jetstreamext.GetBatchOpt{jetstreamext.GetBatchSeq(lo)}
		if len(x.r.Subjects) == 1 {
			opts = append(opts, jetstreamext.GetBatchSubject(x.r.Subjects[0]))
		}
		msgs, err := jetstreamext.GetBatch(x.ctx, x.e.js, x.r.Stream, int(hi-lo+1), opts...) //nolint:gosec // hi-lo < window
		if err != nil {
			return 0, err
		}
		for m, err := range msgs {
			if errors.Is(err, jetstreamext.ErrNoMessages) {
				break
			}
			if err != nil {
				return 0, err
			}
			if m.Sequence > hi {
				break // a sparse subject filter can jump past the window
			}
			x.consider(x.r.Stream, m.Sequence, m.Subject, m.Time, m.Header, m.Data)
		}
		page = x.res.Messages
		x.res.Messages = saved
		// Newest first; keep only what fits, and resume just below the oldest returned message.
		for i := len(page) - 1; i >= 0; i-- {
			if len(x.res.Messages) == x.r.Limit {
				return page[i+1].Seq - 1, nil
			}
			x.res.Messages = append(x.res.Messages, page[i])
		}
		if lo == st.FirstSeq {
			return 0, nil
		}
		hi = lo - 1
	}
	if len(x.res.Messages) == x.r.Limit {
		oldest := x.res.Messages[len(x.res.Messages)-1].Seq
		if oldest <= st.FirstSeq {
			return 0, nil
		}
		return oldest - 1, nil
	}
	if hi < st.FirstSeq {
		return 0, nil
	}
	return hi, nil
}
