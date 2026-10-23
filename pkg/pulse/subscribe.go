package pulse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/aurimasniekis/go-pulse/pkg/pulse/envelope"
)

// Handler processes one event. For durable subscriptions a nil error acks the event and an error naks it for redelivery.
type Handler func(ctx context.Context, e Event) error

// Handle adapts a typed handler: the payload is decoded as JSON into T. Decode failures are passed to fn's error path
// as a terminal error (the event is acked as poison and reported to OnError) rather than redelivered forever.
func Handle[T any](fn func(ctx context.Context, e Event, v T) error) Handler {
	return func(ctx context.Context, e Event) error {
		var v T
		if err := json.Unmarshal(e.Data, &v); err != nil {
			return Terminal(fmt.Errorf("decode %s into %T: %w", e.Subject, v, err))
		}
		return fn(ctx, e, v)
	}
}

type terminalError struct{ err error }

func (t terminalError) Error() string { return t.err.Error() }
func (t terminalError) Unwrap() error { return t.err }

// Terminal marks a handler error as permanent: durable subscriptions term the message instead of redelivering it.
func Terminal(err error) error { return terminalError{err} }

type subOptions struct {
	deliver    jetstream.DeliverPolicy
	since      time.Time
	seq        uint64
	durable    string
	ackWait    time.Duration
	maxDeliver int
	onError    func(Event, error)
	stream     string
}

// SubOption configures Subscribe.
type SubOption func(*subOptions)

// Since replays from a point in time (DeliverByStartTime, PL-BUS-007).
func Since(t time.Time) SubOption {
	return func(o *subOptions) { o.deliver, o.since = jetstream.DeliverByStartTimePolicy, t }
}

// FromSeq replays from a stream sequence.
func FromSeq(seq uint64) SubOption {
	return func(o *subOptions) { o.deliver, o.seq = jetstream.DeliverByStartSequencePolicy, seq }
}

// OnlyNew delivers only events published after subscribing.
func OnlyNew() SubOption { return func(o *subOptions) { o.deliver = jetstream.DeliverNewPolicy } }

// Durable makes the subscription a durable consumer named "<client-id>_<name>" (requires a durable grant).
// The position survives restarts; Since/FromSeq only apply when the consumer is first created.
func Durable(name string) SubOption { return func(o *subOptions) { o.durable = name } }

// AckWait sets how long a durable delivery may take before redelivery (default 30 s).
func AckWait(d time.Duration) SubOption { return func(o *subOptions) { o.ackWait = d } }

// MaxDeliver bounds redeliveries for durable subscriptions (default unlimited).
func MaxDeliver(n int) SubOption { return func(o *subOptions) { o.maxDeliver = n } }

// OnError receives handler errors (and decode failures). Default: ignored.
func OnError(fn func(Event, error)) SubOption { return func(o *subOptions) { o.onError = fn } }

// FromStream subscribes to another stream than the client's default.
func FromStream(name string) SubOption { return func(o *subOptions) { o.stream = name } }

// Subscription is a running subscription.
type Subscription struct {
	cc       jetstream.ConsumeContext
	consumer string
	cancel   context.CancelFunc
	once     sync.Once
}

// Consumer is the JetStream consumer name.
func (s *Subscription) Consumer() string { return s.consumer }

// Stop stops delivery. Durable consumers keep their position on the server.
func (s *Subscription) Stop() {
	s.once.Do(func() {
		s.cc.Stop()
		s.cancel()
	})
}

// Closed is closed once the subscription has fully stopped.
func (s *Subscription) Closed() <-chan struct{} { return s.cc.Closed() }

func toEvent(m jetstream.Msg) Event {
	e := envelope.Decode(m.Subject(), m.Headers(), m.Data())
	if md, err := m.Metadata(); err == nil {
		e.Stream, e.Sequence, e.StreamTime = md.Stream, md.Sequence.Stream, md.Timestamp
	}
	return e
}

// Subscribe delivers events matching filter (under evt.>) to h. Without Durable it uses an ordered consumer
// (in-order, no acks, automatic recovery); with Durable it uses an explicit-ack durable consumer.
func (c *Client) Subscribe(ctx context.Context, filter string, h Handler, opts ...SubOption) (*Subscription, error) {
	o := subOptions{deliver: jetstream.DeliverAllPolicy, stream: c.stream}
	for _, fn := range opts {
		fn(&o)
	}
	if o.stream == DefaultStream {
		if err := envelope.ValidateFilter(filter); err != nil {
			return nil, err
		}
	}
	report := func(e Event, err error) {
		if o.onError != nil {
			o.onError(e, err)
		}
	}
	subCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	var cons jetstream.Consumer
	var err error
	if o.durable == "" {
		occ := jetstream.OrderedConsumerConfig{FilterSubjects: []string{filter}, DeliverPolicy: o.deliver}
		if o.deliver == jetstream.DeliverByStartTimePolicy {
			occ.OptStartTime = &o.since
		}
		if o.deliver == jetstream.DeliverByStartSequencePolicy {
			occ.OptStartSeq = o.seq
		}
		cons, err = c.js.OrderedConsumer(ctx, o.stream, occ)
	} else {
		if c.clientID == "" {
			cancel()
			return nil, errors.New("pulse: durable subscriptions need a client connection from Connect")
		}
		cfg := jetstream.ConsumerConfig{
			Durable:       c.clientID + "_" + o.durable,
			FilterSubject: filter,
			AckPolicy:     jetstream.AckExplicitPolicy,
			DeliverPolicy: o.deliver,
			AckWait:       o.ackWait,
			MaxDeliver:    o.maxDeliver,
		}
		if o.deliver == jetstream.DeliverByStartTimePolicy {
			cfg.OptStartTime = &o.since
		}
		if o.deliver == jetstream.DeliverByStartSequencePolicy {
			cfg.OptStartSeq = o.seq
		}
		cons, err = c.js.CreateOrUpdateConsumer(ctx, o.stream, cfg)
		if err != nil && errors.Is(err, jetstream.ErrConsumerExists) {
			cons, err = c.js.Consumer(ctx, o.stream, cfg.Durable)
		}
	}
	if err != nil {
		cancel()
		return nil, err
	}
	durable := o.durable != ""
	cc, err := cons.Consume(func(m jetstream.Msg) {
		e := toEvent(m)
		herr := h(subCtx, e)
		if !durable {
			if herr != nil {
				report(e, herr)
			}
			return
		}
		var tErr terminalError
		switch {
		case herr == nil:
			_ = m.Ack()
		case errors.As(herr, &tErr):
			report(e, herr)
			_ = m.Term()
		default:
			report(e, herr)
			_ = m.NakWithDelay(backoffFor(m))
		}
	}, jetstream.ConsumeErrHandler(func(_ jetstream.ConsumeContext, err error) {
		report(Event{}, err)
	}))
	if err != nil {
		cancel()
		return nil, err
	}
	return &Subscription{cc: cc, consumer: cons.CachedInfo().Name, cancel: cancel}, nil
}

// backoffFor grows the redelivery delay with the delivery count: 1s, 2s, 4s … capped at 5 minutes.
func backoffFor(m jetstream.Msg) time.Duration {
	n := uint64(1)
	if md, err := m.Metadata(); err == nil && md.NumDelivered > 0 {
		n = md.NumDelivered
	}
	d := time.Second << min(n-1, 9)
	return min(d, 5*time.Minute)
}
