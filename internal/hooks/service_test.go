package hooks

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/aurimasniekis/go-pulse/internal/audit"
	"github.com/aurimasniekis/go-pulse/internal/clients"
	"github.com/aurimasniekis/go-pulse/internal/store"
	"github.com/aurimasniekis/go-pulse/internal/streams"
	"github.com/aurimasniekis/go-pulse/internal/sysapi"
	"github.com/aurimasniekis/go-pulse/internal/testutil/natstest"
)

func setup(t *testing.T) (*Service, jetstream.JetStream, *httptest.Server, *atomic.Int64) {
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
	s := New(db, js, env.MasterKey, nil)
	var fails atomic.Int64
	s.OnFailure = func(string, string) { fails.Add(1) }
	mux := http.NewServeMux()
	mux.Handle("/hooks/{name}", s.Handler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return s, js, srv, &fails
}

func post(t *testing.T, url, body string, hdr map[string]string) *http.Response {
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, url, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func ghSig(secret, body string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

func TestGitHubDeliveryPublishesEvent(t *testing.T) {
	s, js, srv, fails := setup(t)
	ctx := context.Background()
	h, secret, err := s.Create(ctx, Input{Name: "github", Kind: KindGitHub, Enabled: true}, audit.System)
	if err != nil {
		t.Fatal(err)
	}
	if h.Subject != "evt.hook.github.received" || len(secret) < 32 {
		t.Fatalf("%+v %q", h, secret)
	}
	body := `{"ref":"refs/heads/main"}`
	hdr := map[string]string{"X-Hub-Signature-256": ghSig(secret, body), "X-GitHub-Event": "push", "X-GitHub-Delivery": "d-1",
		"Content-Type": "application/json"}
	if r := post(t, srv.URL+"/hooks/github", body, hdr); r.StatusCode != http.StatusAccepted {
		t.Fatalf("valid delivery: %d", r.StatusCode)
	}
	if r := post(t, srv.URL+"/hooks/github", body, hdr); r.StatusCode != http.StatusAccepted { // redelivery
		t.Fatalf("redelivery: %d", r.StatusCode)
	}
	st, _ := js.Stream(ctx, "EVENTS")
	if n := st.CachedInfo().State.Msgs; n != 1 {
		t.Fatalf("stored %d events, want 1 (de-duplicated by delivery id)", n)
	}
	m, _ := st.GetMsg(ctx, 1)
	if m.Header.Get("ce-type") != "github.push" || m.Header.Get("Nats-Msg-Id") != "d-1" || m.Header.Get("ce-source") != "hook:github" ||
		m.Header.Get("X-GitHub-Event") != "push" || string(m.Data) != body {
		t.Fatalf("event headers %v", m.Header)
	}

	hdr["X-Hub-Signature-256"] = ghSig("wrong", body)
	if r := post(t, srv.URL+"/hooks/github", body, hdr); r.StatusCode != http.StatusUnauthorized || fails.Load() != 1 {
		t.Fatalf("bad signature: %d fails %d", r.StatusCode, fails.Load())
	}
	if r := post(t, srv.URL+"/hooks/nope", body, hdr); r.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown hook: %d", r.StatusCode)
	}
	got, _ := s.Get(ctx, h.ID)
	if got.Received != 2 || got.Rejected != 1 || got.LastReceivedAt.IsZero() {
		t.Fatalf("counters %+v", got)
	}

	// Disabled hooks answer 404; rotating the secret invalidates the old one.
	if _, err := s.Update(ctx, h.ID, Input{Name: "github", Kind: KindGitHub, Enabled: false}, audit.System); err != nil {
		t.Fatal(err)
	}
	hdr["X-Hub-Signature-256"] = ghSig(secret, body)
	if r := post(t, srv.URL+"/hooks/github", body, hdr); r.StatusCode != http.StatusNotFound {
		t.Fatalf("disabled hook: %d", r.StatusCode)
	}
	newSecret := "rotated-secret-0123456789" // gitleaks:allow (test literal)
	if _, err := s.Update(ctx, h.ID, Input{Name: "github", Kind: KindGitHub, Enabled: true, Secret: newSecret}, audit.System); err != nil {
		t.Fatal(err)
	}
	if r := post(t, srv.URL+"/hooks/github", body, hdr); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old secret after rotation: %d", r.StatusCode)
	}
	hdr["X-Hub-Signature-256"] = ghSig(newSecret, body)
	hdr["X-GitHub-Delivery"] = "d-2"
	if r := post(t, srv.URL+"/hooks/github", body, hdr); r.StatusCode != http.StatusAccepted {
		t.Fatalf("new secret: %d", r.StatusCode)
	}
}

func TestStripeAndSecretHooks(t *testing.T) {
	s, js, srv, _ := setup(t)
	ctx := context.Background()
	_, secret, err := s.Create(ctx, Input{Name: "stripe", Kind: KindStripe, Enabled: true, Secret: "whsec_0123456789abcdef"}, audit.System) // gitleaks:allow (test literal)
	if err != nil || secret != "whsec_0123456789abcdef" {
		t.Fatalf("%v %q", err, secret)
	}
	body := `{"id":"evt_9","type":"invoice.paid"}`
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(ts + "." + body))
	if r := post(t, srv.URL+"/hooks/stripe", body, map[string]string{"Stripe-Signature": "t=" + ts + ",v1=" + hex.EncodeToString(m.Sum(nil))}); r.StatusCode != http.StatusAccepted {
		t.Fatalf("stripe: %d", r.StatusCode)
	}
	if _, _, err := s.Create(ctx, Input{Name: "simple", Kind: KindSecret, Enabled: true, Secret: "shared-secret-value", Config: Config{Type: "home.alarm.v1"},
		Subject: "evt.home.alarm.triggered"}, audit.System); err != nil {
		t.Fatal(err)
	}
	if r := post(t, srv.URL+"/hooks/simple", "{}", map[string]string{"X-Webhook-Secret": "shared-secret-value"}); r.StatusCode != http.StatusAccepted {
		t.Fatalf("secret hook: %d", r.StatusCode)
	}
	st, _ := js.Stream(ctx, "EVENTS")
	m1, _ := st.GetMsg(ctx, 1)
	m2, _ := st.GetMsg(ctx, 2)
	if m1.Header.Get("ce-type") != "stripe.invoice.paid" || m1.Header.Get("Nats-Msg-Id") != "evt_9" {
		t.Fatalf("stripe event %v", m1.Header)
	}
	if m2.Subject != "evt.home.alarm.triggered" || m2.Header.Get("ce-type") != "home.alarm.v1" {
		t.Fatalf("secret event %s %v", m2.Subject, m2.Header)
	}
	for name, in := range map[string]Input{
		"bad kind":     {Name: "x", Kind: "aws"},
		"bad name":     {Name: "X Y", Kind: KindGitHub},
		"bad subject":  {Name: "y", Kind: KindGitHub, Subject: "evt.a"},
		"short secret": {Name: "z", Kind: KindSecret, Secret: "short"},
		"duplicate":    {Name: "simple", Kind: KindSecret},
	} {
		if _, _, err := s.Create(ctx, in, audit.System); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
