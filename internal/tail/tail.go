// Package tail runs live tail sessions (PL-EXP-007): one ordered DeliverNew consumer per session, at most MaxSessions
// sessions, per-session rate limiting with sampling notices.
package tail

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/aurimasniekis/go-pulse/internal/query"
)

// Defaults from the plan.
const (
	DefaultMaxSessions = 10
	DefaultRate        = 200 // messages per second per session
	Keepalive          = 20 * time.Second
)

// ErrTooMany is returned when MaxSessions sessions are open.
var ErrTooMany = errors.New("too many live tail sessions")

// Item is one tail output: either a message or a sampling notice.
type Item struct {
	Msg    *query.Message
	Notice *Notice
}

// Notice tells the client that messages were dropped to respect the rate limit or a slow reader.
type Notice struct {
	Dropped int    `json:"dropped"`
	Reason  string `json:"reason"` // "rate" | "slow_client"
	Rate    int    `json:"rate"`
}

// Manager limits and creates sessions.
type Manager struct {
	js     jetstream.JetStream
	max    int
	rate   int
	active atomic.Int32
}

// New returns a manager.
func New(js jetstream.JetStream, maxSessions, rate int) *Manager {
	if maxSessions <= 0 {
		maxSessions = DefaultMaxSessions
	}
	if rate <= 0 {
		rate = DefaultRate
	}
	return &Manager{js: js, max: maxSessions, rate: rate}
}

// Active is the number of open sessions.
func (m *Manager) Active() int { return int(m.active.Load()) }

// Request starts a session.
type Request struct {
	Stream   string
	Subjects []string
	FromSeq  uint64    // resume (Last-Event-ID + 1); 0 means new messages only
	Since    time.Time // optional replay start
}

// Session is an open tail.
type Session struct {
	items   chan Item
	cc      jetstream.ConsumeContext
	release func()
	once    sync.Once

	rate    int
	mu      sync.Mutex
	window  time.Time
	count   int
	dropped map[string]int
}

// Items delivers messages and notices until Close.
func (s *Session) Items() <-chan Item { return s.items }

// Close stops the consumer and frees the slot.
func (s *Session) Close() {
	s.once.Do(func() {
		s.cc.Stop()
		s.release()
	})
}

// Open starts a session. The caller must Close it.
func (m *Manager) Open(ctx context.Context, r Request) (*Session, error) {
	if n := m.active.Add(1); int(n) > m.max {
		m.active.Add(-1)
		return nil, ErrTooMany
	}
	release := func() { m.active.Add(-1) }
	cfg := jetstream.OrderedConsumerConfig{FilterSubjects: r.Subjects, DeliverPolicy: jetstream.DeliverNewPolicy, InactiveThreshold: 30 * time.Second}
	switch {
	case r.FromSeq > 0:
		cfg.DeliverPolicy, cfg.OptStartSeq = jetstream.DeliverByStartSequencePolicy, r.FromSeq
	case !r.Since.IsZero():
		t := r.Since
		cfg.DeliverPolicy, cfg.OptStartTime = jetstream.DeliverByStartTimePolicy, &t
	}
	cons, err := m.js.OrderedConsumer(ctx, r.Stream, cfg)
	if err != nil {
		release()
		return nil, err
	}
	s := &Session{items: make(chan Item, 256), release: release, dropped: map[string]int{}, rate: m.rate}
	s.cc, err = cons.Consume(func(msg jetstream.Msg) {
		md, err := msg.Metadata()
		if err != nil {
			return
		}
		s.deliver(query.Message{Stream: md.Stream, Seq: md.Sequence.Stream, Subject: msg.Subject(), Time: md.Timestamp.UTC(),
			Headers: msg.Headers(), Data: msg.Data(), Size: len(msg.Data()) + len(msg.Subject())})
	})
	if err != nil {
		release()
		return nil, err
	}
	return s, nil
}

// deliver applies the per-second rate limit and never blocks the consumer: overflow is counted and reported.
func (s *Session) deliver(m query.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if now.Sub(s.window) >= time.Second {
		s.flushNotice()
		s.window, s.count = now, 0
	}
	if s.count >= s.rate {
		s.dropped["rate"]++
		return
	}
	select {
	case s.items <- Item{Msg: &m}:
		s.count++
	default:
		s.dropped["slow_client"]++
	}
}

func (s *Session) flushNotice() {
	for reason, n := range s.dropped {
		if n == 0 {
			continue
		}
		select {
		case s.items <- Item{Notice: &Notice{Dropped: n, Reason: reason, Rate: s.rate}}:
			s.dropped[reason] = 0
		default:
		}
	}
}

// Flush emits pending drop notices (called by the SSE loop on its keepalive tick so quiet streams still report drops).
func (s *Session) Flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushNotice()
}
