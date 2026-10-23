package outbound

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/aurimasniekis/go-pulse/internal/audit"
	"github.com/aurimasniekis/go-pulse/internal/clients"
	"github.com/aurimasniekis/go-pulse/internal/store"
	"github.com/aurimasniekis/go-pulse/internal/streams"
	"github.com/aurimasniekis/go-pulse/internal/sysapi"
	"github.com/aurimasniekis/go-pulse/internal/testutil/natstest"
)

type got struct {
	body    string
	headers http.Header
}

type sink struct {
	mu     sync.Mutex
	reqs   []got
	status func(n int) int
	ch     chan struct{}
}

func newSink(status func(n int) int) (*sink, *httptest.Server) {
	s := &sink{status: status, ch: make(chan struct{}, 100)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.reqs = append(s.reqs, got{string(b), r.Header.Clone()})
		n := len(s.reqs)
		s.mu.Unlock()
		w.WriteHeader(s.status(n))
		_, _ = w.Write([]byte("response " + string(b)))
		s.ch <- struct{}{}
	}))
	return s, srv
}

func (s *sink) wait(t *testing.T, n int) []got {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		s.mu.Lock()
		have := len(s.reqs)
		s.mu.Unlock()
		if have >= n {
			s.mu.Lock()
			defer s.mu.Unlock()
			return append([]got(nil), s.reqs...)
		}
		select {
		case <-s.ch:
		case <-deadline:
			t.Fatalf("got %d requests, want %d", have, n)
		}
	}
}

type fx struct {
	js  jetstream.JetStream
	db  *store.DB
	svc *Service
}

func setup(t *testing.T, allow []string) *fx {
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
	svc, err := New(db, js, env.MasterKey, Options{Backoff: []time.Duration{50 * time.Millisecond, 50 * time.Millisecond}, Allow: allow, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	rctx, cancel := context.WithCancel(ctx)
	if err := svc.Start(rctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); svc.Wait() })
	return &fx{js: js, db: db, svc: svc}
}

func (f *fx) pub(t *testing.T, subject, data string) {
	t.Helper()
	m := nats.NewMsg(subject)
	m.Data = []byte(data)
	m.Header.Set("ce-type", "test.v1")
	m.Header.Set("content-type", "application/json")
	if _, err := f.js.PublishMsg(context.Background(), m); err != nil {
		t.Fatal(err)
	}
}

func TestDeliveryOrderSignatureAndRetry(t *testing.T) {
	f := setup(t, []string{"127.0.0.0/8"})
	ctx := context.Background()
	// First request fails, everything after succeeds: event 1 is retried before event 2 goes out (order kept).
	sk, srv := newSink(func(n int) int {
		if n == 1 {
			return http.StatusInternalServerError
		}
		return http.StatusOK
	})
	defer srv.Close()
	h, secret, err := f.svc.Create(ctx, Input{Name: "sink", Filter: "evt.out.>", URL: srv.URL, Headers: map[string]string{"X-Custom": "1"}}, audit.System)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(secret, "whsec_") {
		t.Fatalf("secret %q", secret)
	}
	f.pub(t, "evt.out.thing.one", `{"n":1}`)
	f.pub(t, "evt.out.thing.two", `{"n":2}`)
	f.pub(t, "evt.other.thing.x", `{"n":3}`) // filtered out
	reqs := sk.wait(t, 3)
	if reqs[0].body != `{"n":1}` || reqs[1].body != `{"n":1}` || reqs[2].body != `{"n":2}` {
		t.Fatalf("order: %q %q %q", reqs[0].body, reqs[1].body, reqs[2].body)
	}
	r := reqs[1]
	if r.headers.Get("Pulse-Delivery-Attempt") != "2" || r.headers.Get("X-Custom") != "1" || r.headers.Get("Pulse-Event-Type") != "test.v1" ||
		r.headers.Get("Content-Type") != "application/json" || r.headers.Get("Pulse-Subject") != "evt.out.thing.one" {
		t.Fatalf("headers %v", r.headers)
	}
	// Verify the signature as a receiver would.
	sig := r.headers.Get("Pulse-Signature")
	ts := strings.TrimPrefix(strings.Split(sig, ",")[0], "t=")
	if want := Sign([]byte(secret), []byte(r.body), time.Unix(atoi(ts), 0)); want != sig {
		t.Fatalf("signature %s want %s", sig, want)
	}
	time.Sleep(100 * time.Millisecond)
	ds, _ := f.svc.Deliveries(ctx, h.ID, 0, 10)
	if len(ds) != 3 || ds[2].Outcome != "retry" || ds[2].StatusCode != 500 || ds[1].Outcome != "delivered" || ds[0].Seq != 2 {
		t.Fatalf("log %+v", ds)
	}
	if !strings.Contains(ds[0].ResponseExcerpt, `{"n":2}`) {
		t.Fatalf("excerpt %q", ds[0].ResponseExcerpt)
	}
}

func atoi(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

func TestDeadLetterThenNext(t *testing.T) {
	f := setup(t, []string{"127.0.0.0/8"})
	ctx := context.Background()
	sk, srv := newSink(func(n int) int {
		if n <= 3 {
			return http.StatusBadGateway // all 3 attempts of event 1 fail
		}
		return http.StatusNoContent
	})
	defer srv.Close()
	h, _, err := f.svc.Create(ctx, Input{Name: "dl", Filter: "evt.dl.>", URL: srv.URL}, audit.System)
	if err != nil {
		t.Fatal(err)
	}
	f.pub(t, "evt.dl.a.b", "1")
	f.pub(t, "evt.dl.a.b", "2")
	reqs := sk.wait(t, 4)
	if reqs[3].body != "2" {
		t.Fatalf("after dead letter: %q", reqs[3].body)
	}
	time.Sleep(100 * time.Millisecond)
	ds, _ := f.svc.Deliveries(ctx, h.ID, 0, 10)
	outcomes := []string{}
	for i := len(ds) - 1; i >= 0; i-- {
		outcomes = append(outcomes, ds[i].Outcome)
	}
	if strings.Join(outcomes, ",") != "retry,retry,dead,delivered" {
		t.Fatalf("outcomes %v", outcomes)
	}
	// Manual retry of the dead letter delivers it once more.
	d, err := f.svc.Retry(ctx, ds[1].ID, audit.System)
	if err != nil || d.Outcome != "manual" || d.StatusCode != http.StatusNoContent {
		t.Fatalf("retry: %+v %v", d, err)
	}
	if reqs := sk.wait(t, 5); reqs[4].body != "1" {
		t.Fatalf("manual retry body %q", reqs[4].body)
	}
}

func TestPauseResumeKeepsPosition(t *testing.T) {
	f := setup(t, []string{"127.0.0.0/8"})
	ctx := context.Background()
	sk, srv := newSink(func(int) int { return http.StatusOK })
	defer srv.Close()
	h, _, err := f.svc.Create(ctx, Input{Name: "pz", Filter: "evt.pz.>", URL: srv.URL}, audit.System)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.SetPaused(ctx, h.ID, true, audit.System); err != nil {
		t.Fatal(err)
	}
	f.pub(t, "evt.pz.a.b", "1")
	f.pub(t, "evt.pz.a.b", "2")
	time.Sleep(300 * time.Millisecond)
	sk.mu.Lock()
	n := len(sk.reqs)
	sk.mu.Unlock()
	if n != 0 {
		t.Fatalf("delivered while paused: %d", n)
	}
	got, _ := f.svc.Get(ctx, h.ID)
	if !got.Paused || got.Pending != 2 {
		t.Fatalf("paused hook %+v", got)
	}
	if _, err := f.svc.SetPaused(ctx, h.ID, false, audit.System); err != nil {
		t.Fatal(err)
	}
	if reqs := sk.wait(t, 2); reqs[0].body != "1" || reqs[1].body != "2" {
		t.Fatalf("after resume %q %q", reqs[0].body, reqs[1].body)
	}
}

func TestSSRFGuard(t *testing.T) {
	f := setup(t, nil) // loopback not allowlisted
	ctx := context.Background()
	if _, _, err := f.svc.Create(ctx, Input{Name: "meta", Filter: "evt.x.>", URL: "http://169.254.169.254/latest"}, audit.System); !errors.Is(err, ErrInvalid) {
		t.Fatalf("metadata URL: %v", err)
	}
	_, srv := newSink(func(int) int { return http.StatusOK })
	defer srv.Close()
	// A loopback literal is refused at creation…
	if _, _, err := f.svc.Create(ctx, Input{Name: "lo", Filter: "evt.x.>", URL: srv.URL}, audit.System); !errors.Is(err, ErrInvalid) {
		t.Fatalf("loopback literal: %v", err)
	}
	// …and a hostname that resolves to loopback is refused at dial time.
	host := strings.Replace(srv.URL, "127.0.0.1", "localhost.", 1)
	h, _, err := f.svc.Create(ctx, Input{Name: "lo2", Filter: "evt.x.>", URL: host}, audit.System)
	if err != nil {
		t.Fatal(err)
	}
	d, err := f.svc.Test(ctx, h.ID, audit.System)
	if err != nil || d.StatusCode != 0 || !strings.Contains(d.Error, "blocked") {
		t.Fatalf("test delivery: %+v %v", d, err)
	}
}

func TestValidationAndDelete(t *testing.T) {
	f := setup(t, []string{"127.0.0.0/8"})
	ctx := context.Background()
	for name, in := range map[string]Input{
		"bad name":        {Name: "Bad", Filter: "evt.>", URL: "https://example.com"},
		"bad filter":      {Name: "a", Filter: "other.>", URL: "https://example.com"},
		"bad url":         {Name: "b", Filter: "evt.>", URL: "ftp://example.com"},
		"reserved header": {Name: "c", Filter: "evt.>", URL: "https://example.com", Headers: map[string]string{"Pulse-Signature": "x"}},
		"short secret":    {Name: "d", Filter: "evt.>", URL: "https://example.com", Secret: "short"},
	} {
		if _, _, err := f.svc.Create(ctx, in, audit.System); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	h, _, err := f.svc.Create(ctx, Input{Name: "gone", Filter: "evt.>", URL: "https://example.com"}, audit.System)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Delete(ctx, h.ID, audit.System); err != nil {
		t.Fatal(err)
	}
	if _, err := f.js.Consumer(ctx, "EVENTS", consumerName(h.ID)); !errors.Is(err, jetstream.ErrConsumerNotFound) {
		t.Fatalf("consumer left behind: %v", err)
	}
	if _, err := f.svc.Get(ctx, h.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get deleted: %v", err)
	}
	b, _ := json.Marshal(DefaultBackoff)
	if len(DefaultBackoff) != 11 || DefaultBackoff[0] != time.Second || DefaultBackoff[10] != time.Hour {
		t.Fatalf("default backoff %s", b)
	}
}
