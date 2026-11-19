package pulse_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/aurimasniekis/go-pulse/internal/audit"
	"github.com/aurimasniekis/go-pulse/internal/clients"
	"github.com/aurimasniekis/go-pulse/internal/natsauth"
	"github.com/aurimasniekis/go-pulse/internal/store"
	"github.com/aurimasniekis/go-pulse/internal/streams"
	"github.com/aurimasniekis/go-pulse/internal/sysapi"
	"github.com/aurimasniekis/go-pulse/internal/testutil/natstest"
	"github.com/aurimasniekis/go-pulse/pkg/pulse"
)

type world struct {
	env *natstest.Env
	svc *clients.Service
	app *pulse.Client
}

func newWorld(t *testing.T) *world {
	t.Helper()
	env := natstest.Start(t)
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{Path: env.Layout.DatabaseFile()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	svc := clients.New(db, env.Keys, sysapi.New(env.SysConn()), nil)
	if _, err := svc.Sync(ctx, false); err != nil {
		t.Fatal(err)
	}
	app, err := pulse.New(env.AppConn(), "pulsed-test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := streams.EnsureEvents(ctx, app.JetStream(), 256<<20, false, nil); err != nil {
		t.Fatal(err)
	}
	if err := streams.EnsureBlobs(ctx, app.JetStream()); err != nil {
		t.Fatal(err)
	}
	return &world{env: env, svc: svc, app: app}
}

func (w *world) client(t *testing.T, name string, grants ...natsauth.Grant) *pulse.Client {
	t.Helper()
	_, creds, err := w.svc.Create(context.Background(), clients.CreateInput{Name: name, Grants: grants}, audit.System)
	if err != nil {
		t.Fatal(err)
	}
	c, err := pulse.Connect(w.env.ClientURL, pulse.WithCreds(creds), pulse.WithCA([]byte(w.env.Keys.CACert)), pulse.WithSource(name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func pub(s string) natsauth.Grant { return natsauth.Grant{Action: natsauth.ActPublish, Subject: s} }
func sub(s string) natsauth.Grant { return natsauth.Grant{Action: natsauth.ActSubscribe, Subject: s} }

type collector struct {
	mu     sync.Mutex
	events []pulse.Event
	ch     chan struct{}
}

func newCollector() *collector { return &collector{ch: make(chan struct{}, 1024)} }

func (c *collector) handle(_ context.Context, e pulse.Event) error {
	c.mu.Lock()
	c.events = append(c.events, e)
	c.mu.Unlock()
	c.ch <- struct{}{}
	return nil
}

func (c *collector) wait(t *testing.T, n int) []pulse.Event {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		c.mu.Lock()
		got := len(c.events)
		c.mu.Unlock()
		if got >= n {
			c.mu.Lock()
			defer c.mu.Unlock()
			return append([]pulse.Event(nil), c.events...)
		}
		select {
		case <-c.ch:
		case <-deadline:
			t.Fatalf("got %d events, want %d", got, n)
		}
	}
}

func TestPublishReplaySince(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	c := w.client(t, "producer", pub("evt.test.>"), sub("evt.test.>"))
	if c.ClientID() == "" {
		t.Fatal("client id not derived from creds")
	}
	for i := range 3 {
		ack, err := c.Publish(ctx, "evt.test.thing.created", "test.thing.created.v1", map[string]int{"n": i})
		if err != nil || ack.Sequence != uint64(i+1) {
			t.Fatalf("publish %d: %+v %v", i, ack, err)
		}
	}
	time.Sleep(20 * time.Millisecond)
	cut := time.Now()
	time.Sleep(20 * time.Millisecond)
	for i := 3; i < 5; i++ {
		if _, err := c.Publish(ctx, "evt.test.thing.updated", "test.thing.updated.v1", map[string]int{"n": i}); err != nil {
			t.Fatal(err)
		}
	}
	all := newCollector()
	s1, err := c.Subscribe(ctx, "evt.test.>", all.handle)
	if err != nil {
		t.Fatal(err)
	}
	defer s1.Stop()
	evs := all.wait(t, 5)
	for i, e := range evs {
		if e.Sequence != uint64(i+1) || e.Source != "producer" || e.ContentType != "application/json" || e.Stream != "EVENTS" {
			t.Fatalf("event %d: %+v", i, e)
		}
	}
	since := newCollector()
	s2, err := c.Subscribe(ctx, "evt.test.>", since.handle, pulse.Since(cut))
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Stop()
	evs = since.wait(t, 2)
	time.Sleep(100 * time.Millisecond)
	if len(since.events) != 2 || evs[0].Sequence != 4 || evs[0].Type != "test.thing.updated.v1" {
		t.Fatalf("since replay: %d events, first %+v", len(since.events), evs[0])
	}
}

func TestDeduplicationAndValidation(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	c := w.client(t, "dedupe", pub("evt.test.>"))
	a1, err := c.Publish(ctx, "evt.test.order.paid", "order.paid", "x", pulse.WithID("fixed-id-1"))
	if err != nil {
		t.Fatal(err)
	}
	a2, err := c.Publish(ctx, "evt.test.order.paid", "order.paid", "x", pulse.WithID("fixed-id-1"))
	if err != nil || !a2.Duplicate || a2.Sequence != a1.Sequence {
		t.Fatalf("dedupe: %+v %v", a2, err)
	}
	if _, err := c.Publish(ctx, "evt.test.bad", "x", nil); err == nil {
		t.Fatal("published to a 3-token subject")
	}
	// Publishing outside the grant fails (no JetStream ack arrives: permission violation).
	tctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if _, err := c.Publish(tctx, "evt.other.order.paid", "order.paid", "x"); err == nil {
		t.Fatal("published outside grant")
	}
}

func TestSubscribeOutsideGrantFails(t *testing.T) {
	w := newWorld(t)
	c := w.client(t, "narrow", sub("evt.home.>"))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	if _, err := c.Subscribe(ctx, "evt.work.>", newCollector().handle); err == nil {
		t.Fatal("subscribed outside grant")
	}
	t.Logf("denied subscribe returned after %v", time.Since(start))
	// A narrower filter inside the grant works.
	s, err := c.Subscribe(context.Background(), "evt.home.door.>", newCollector().handle)
	if err != nil {
		t.Fatalf("narrower filter: %v", err)
	}
	s.Stop()
}

type door struct {
	Door string `json:"door"`
}

func TestDurableTypedHandlerAndRedelivery(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	c := w.client(t, "worker", pub("evt.home.>"),
		natsauth.Grant{Action: natsauth.ActDurable, Subject: "evt.home.>", Name: "doors"})
	for _, d := range []string{"front", "back"} {
		if _, err := c.Publish(ctx, "evt.home.door.opened", "home.door.opened.v1", door{d}); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	seen := map[string]int{}
	got := make(chan string, 16)
	h := pulse.Handle(func(_ context.Context, _ pulse.Event, v door) error {
		mu.Lock()
		seen[v.Door]++
		n := seen[v.Door]
		mu.Unlock()
		if v.Door == "back" && n == 1 {
			return errors.New("transient")
		}
		got <- v.Door
		return nil
	})
	// Start from a time: resuming below without it must bind to the existing durable, not try to update its start
	// position (regression: "deliver policy can not be updated").
	s, err := c.Subscribe(ctx, "evt.home.>", h, pulse.Durable("doors"), pulse.Since(time.Now().Add(-time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"front": true, "back": true}
	for len(want) > 0 {
		select {
		case d := <-got:
			delete(want, d)
		case <-time.After(5 * time.Second):
			t.Fatalf("missing %v", want)
		}
	}
	if seen["back"] != 2 {
		t.Fatalf("back delivered %d times, want 2 (nak → redelivery)", seen["back"])
	}
	s.Stop()
	<-s.Closed()
	// Position survives: a new subscription sees only the new event.
	if _, err := c.Publish(ctx, "evt.home.door.opened", "home.door.opened.v1", door{"side"}); err != nil {
		t.Fatal(err)
	}
	s2, err := c.Subscribe(ctx, "evt.home.>", h, pulse.Durable("doors"))
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Stop()
	select {
	case d := <-got:
		if d != "side" {
			t.Fatalf("durable resumed at %q", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no delivery after resume")
	}
	if s2.Consumer() != c.ClientID()+"_doors" {
		t.Fatalf("consumer name %s", s2.Consumer())
	}
	// Another durable name is not granted.
	if _, err := c.Subscribe(ctx, "evt.home.>", h, pulse.Durable("other")); err == nil {
		t.Fatal("ungranted durable created")
	}
}

const drainAckWait = 3 * time.Second

func TestDrainDoesNotStrandEventsUntilAckWait(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	c := w.client(t, "drainer", pub("evt.home.>"),
		natsauth.Grant{Action: natsauth.ActDurable, Subject: "evt.home.>", Name: "drain"})
	var mu sync.Mutex
	seen := map[string]bool{}
	h := pulse.Handle(func(_ context.Context, _ pulse.Event, v door) error {
		mu.Lock()
		seen[v.Door] = true
		mu.Unlock()
		return nil
	})
	subscribe := func() *pulse.Subscription {
		s, err := c.Subscribe(ctx, "evt.home.>", h, pulse.Durable("drain"), pulse.AckWait(drainAckWait), pulse.OnlyNew())
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	publish := func(name string) {
		if _, err := c.Publish(ctx, "evt.home.door.opened", "home.door.opened.v1", door{name}); err != nil {
			t.Error(err)
		}
	}
	const iterations, perIteration = 25, 8
	var want []string
	s := subscribe()
	for i := 0; i < iterations; i++ {
		var wg sync.WaitGroup
		wg.Add(1)
		started := make(chan struct{})
		go func() {
			defer wg.Done()
			for j := 0; j < perIteration; j++ {
				name := fmt.Sprintf("d%d-%d", i, j)
				mu.Lock()
				want = append(want, name)
				mu.Unlock()
				publish(name)
				if j == 1 {
					close(started)
				}
			}
		}()
		<-started
		dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := s.Drain(dctx)
		cancel()
		if err != nil {
			t.Fatalf("iteration %d: drain: %v", i, err)
		}
		wg.Wait()
		resumed := time.Now()
		s = subscribe()
		// Everything must arrive well before AckWait; a stranded event would take the full AckWait.
		deadline := time.Now().Add(drainAckWait / 2)
		for {
			mu.Lock()
			missing := 0
			for _, n := range want {
				if !seen[n] {
					missing++
				}
			}
			mu.Unlock()
			if missing == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("iteration %d: %d events not delivered %v after resume (AckWait %v)", i, missing, time.Since(resumed), drainAckWait)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	s.Stop()
}

func TestDrainContextTimeoutCancelsHandlerContext(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	c := w.client(t, "slow", pub("evt.home.>"),
		natsauth.Grant{Action: natsauth.ActDurable, Subject: "evt.home.>", Name: "slow"})
	entered := make(chan struct{})
	release := make(chan struct{})
	handlerCtxErr := make(chan error, 1)
	var once sync.Once
	s, err := c.Subscribe(ctx, "evt.home.>", func(hctx context.Context, _ pulse.Event) error {
		first := false
		once.Do(func() { first = true; close(entered) })
		<-release
		if first {
			handlerCtxErr <- hctx.Err()
		}
		return nil
	}, pulse.Durable("slow"), pulse.OnlyNew())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Publish(ctx, "evt.home.door.opened", "home.door.opened.v1", door{"x"}); err != nil {
		t.Fatal(err)
	}
	<-entered
	dctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := s.Drain(dctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Drain = %v, want deadline exceeded", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Drain took %v after a 100ms ctx", d)
	}
	close(release)
	if err := <-handlerCtxErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("handler ctx err = %v, want canceled", err)
	}
	select {
	case <-s.Closed():
	case <-time.After(5 * time.Second):
		t.Fatal("subscription not closed after Drain ctx timeout")
	}
	// Idempotent after Drain/Stop, and nil-safe.
	if err := s.Drain(ctx); err != nil {
		t.Fatalf("second Drain = %v", err)
	}
	s.Stop()
	var nilSub *pulse.Subscription
	if err := nilSub.Drain(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRawPublishIsUntyped(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	if err := w.app.Conn().Publish("evt.raw.thing.happened", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	c := w.client(t, "reader", sub("evt.raw.>"))
	col := newCollector()
	s, err := c.Subscribe(ctx, "evt.raw.>", col.handle)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	e := col.wait(t, 1)[0]
	if !e.Untyped() || string(e.Data) != "hello" {
		t.Fatalf("%+v", e)
	}
}

func TestBlobs(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	c := w.client(t, "camera", pub("evt.camera.>"), sub("evt.camera.>"), natsauth.Grant{Action: natsauth.ActBlobs})
	payload := bytes.Repeat([]byte("0123456789abcdef"), 200_000) // 3.2 MB > max_payload
	ref, err := c.PutBlob(ctx, bytes.NewReader(payload), ".bin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Publish(ctx, "evt.camera.snapshot.taken", "camera.snapshot.taken.v1", nil, pulse.WithBlob(*ref)); err != nil {
		t.Fatal(err)
	}
	col := newCollector()
	s, err := c.Subscribe(ctx, "evt.camera.>", col.handle)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	e := col.wait(t, 1)[0]
	if e.Blob == nil || e.Blob.Size != int64(len(payload)) {
		t.Fatalf("blob ref %+v", e.Blob)
	}
	got, err := c.GetBlob(ctx, *e.Blob)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("get blob: %v", err)
	}
	// A client without the blobs grant cannot read the bucket.
	other := w.client(t, "noblobs", sub("evt.camera.>"))
	tctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err := other.GetBlob(tctx, *ref); err == nil {
		t.Fatal("read blob without grant")
	}
	_ = jetstream.ErrObjectNotFound
}
