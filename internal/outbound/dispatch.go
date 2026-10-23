package outbound

import (
	"context"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/aurimasniekis/go-pulse/pkg/pulse/envelope"
)

type runner struct {
	cc     jetstream.ConsumeContext
	cancel context.CancelFunc
	done   chan struct{}
}

// Start runs dispatchers for every active hook until ctx ends, and prunes the delivery log daily.
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	s.ctx = ctx
	s.mu.Unlock()
	ids, err := s.ids(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		s.reconcile(id) //nolint:contextcheck // reconcile uses s.ctx, which is this ctx
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		t := time.NewTicker(24 * time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := s.PruneDeliveries(ctx); err != nil {
					s.log.Warn("prune deliveries", "err", err)
				}
			}
		}
	}()
	return nil
}

// Wait stops every dispatcher (after ctx ended) and waits for them.
func (s *Service) Wait() {
	s.mu.Lock()
	for id := range s.runners {
		s.stopLocked(id)
	}
	s.mu.Unlock()
	s.wg.Wait()
}

// reconcile starts or stops the dispatcher of one hook to match its stored state.
func (s *Service) reconcile(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx == nil || s.ctx.Err() != nil {
		return
	}
	r, err := s.load(s.ctx, id)
	if err != nil || r.Paused {
		s.stopLocked(id)
		return
	}
	if _, running := s.runners[id]; running {
		return
	}
	if err := s.startLocked(r); err != nil {
		s.log.Warn("start dispatcher", "hook", r.Name, "err", err)
	}
}

func (s *Service) restart(id string) {
	s.stop(id)
	s.reconcile(id)
}

func (s *Service) stop(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked(id)
}

func (s *Service) stopLocked(id string) {
	if r, ok := s.runners[id]; ok {
		r.cc.Stop()
		r.cancel()
		<-r.done
		delete(s.runners, id)
	}
}

func (s *Service) startLocked(h *row) error {
	ctx, cancel := context.WithCancel(s.ctx)
	cons, err := s.js.Consumer(ctx, stream, consumerName(h.ID))
	if err != nil {
		// Recreate a missing consumer (e.g. restored stream); it starts at new events.
		cons, err = s.js.CreateOrUpdateConsumer(ctx, stream, consumerConfig(h.ID, h.Name, h.Filter, s.backoff))
		if err != nil {
			cancel()
			return err
		}
	}
	r := &runner{cancel: cancel, done: make(chan struct{})}
	hookID := h.ID
	work := make(chan jetstream.Msg, 1)
	r.cc, err = cons.Consume(func(m jetstream.Msg) {
		select {
		case work <- m:
		case <-ctx.Done():
		}
	}, jetstream.PullMaxMessages(1))
	if err != nil {
		cancel()
		return err
	}
	s.runners[hookID] = r
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer close(r.done)
		for {
			select {
			case <-ctx.Done():
				return
			case m := <-work:
				s.handle(ctx, hookID, m)
			}
		}
	}()
	return nil
}

// handle delivers one message and acks, naks with backoff, or terms it after the last attempt.
func (s *Service) handle(ctx context.Context, hookID string, m jetstream.Msg) {
	md, err := m.Metadata()
	if err != nil {
		_ = m.Term()
		return
	}
	// Reload per message so URL, header and secret changes apply immediately.
	r, err := s.load(ctx, hookID)
	if err != nil {
		_ = m.NakWithDelay(time.Minute)
		return
	}
	e := envelope.Decode(m.Subject(), m.Headers(), m.Data())
	e.Sequence, e.Stream = md.Sequence.Stream, md.Stream
	attempt := int(md.NumDelivered) //nolint:gosec // bounded by MaxDeliver
	d := s.attempt(ctx, r, e, attempt)
	switch {
	case ok(d):
		d.Outcome = "delivered"
		_ = m.Ack()
	case attempt >= s.MaxAttempts():
		d.Outcome = "dead"
		_ = m.Term()
		s.log.Warn("webhook delivery gave up", "hook", r.Name, "seq", e.Sequence, "error", d.Error)
	default:
		d.Outcome = "retry"
		_ = m.NakWithDelay(s.backoff[attempt-1])
	}
	if ctx.Err() == nil {
		s.record(ctx, d)
	}
}
