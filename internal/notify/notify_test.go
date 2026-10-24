package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const botToken = "123456:test-bot-token" // gitleaks:allow (test literal)

func TestTelegramFormatAndSecrecy(t *testing.T) {
	var got map[string]any
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	tg := &Telegram{APIURL: srv.URL, Token: botToken, ChatID: "-100", HTTP: srv.Client()}
	err := tg.Send(context.Background(), Message{Title: "Cert <expiry>", Text: "a & b", Severity: Critical, State: "firing", Key: "origin", At: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/bot"+botToken+"/sendMessage" || got["chat_id"] != "-100" || got["parse_mode"] != "HTML" {
		t.Fatalf("%s %v", path, got)
	}
	text := got["text"].(string)
	if !strings.Contains(text, "🔴 FIRING [CRITICAL] Cert &lt;expiry&gt;") || !strings.Contains(text, "a &amp; b") || !strings.Contains(text, "<code>origin</code>") {
		t.Fatalf("text %q", text)
	}
	// Transport errors must not leak the token (it is part of the URL).
	srv.Close()
	err = tg.Send(context.Background(), Message{Title: "x", Severity: Info, State: "test"})
	if err == nil || strings.Contains(err.Error(), botToken) || strings.Contains(err.Error(), "bot123") {
		t.Fatalf("error leaks the token or is nil: %v", err)
	}
}

func TestDiscordFormat(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	d := &Discord{WebhookURL: srv.URL + "/api/webhooks/1/secret", HTTP: srv.Client()}
	if err := d.Send(context.Background(), Message{Title: "Ban storm", Text: "12 bans", Severity: Warning, State: "resolved", At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	emb := got["embeds"].([]any)[0].(map[string]any)
	if emb["color"].(float64) != 0x2F9E44 || !strings.HasPrefix(emb["title"].(string), "✅ RESOLVED [WARNING] Ban storm") || emb["description"] != "12 bans" {
		t.Fatalf("%v", got)
	}
	if am := got["allowed_mentions"].(map[string]any); len(am["parse"].([]any)) != 0 {
		t.Fatalf("mentions allowed: %v", am)
	}
}

func TestRetryOn429AndPermanentErrors(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	n := New("d", &Discord{WebhookURL: srv.URL, HTTP: srv.Client()}, Options{MinSeverity: Info})
	if err := n.Test(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || n.Status().Sent != 1 {
		t.Fatalf("calls %d status %+v", calls.Load(), n.Status())
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"ok":false,"description":"Unauthorized"}`))
	}))
	defer bad.Close()
	n2 := New("t", &Telegram{APIURL: bad.URL, Token: botToken, ChatID: "1", HTTP: bad.Client()}, Options{})
	err := n2.Test(context.Background())
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") || strings.Contains(err.Error(), botToken) {
		t.Fatalf("%v", err)
	}
	if st := n2.Status(); st.Failed != 1 || st.LastError == "" {
		t.Fatalf("%+v", st)
	}
}

type countSender struct{ n atomic.Int32 }

func (c *countSender) Kind() string { return "discord" }
func (c *countSender) Send(context.Context, Message) error {
	c.n.Add(1)
	return nil
}

func TestSeverityFloorAndRateLimit(t *testing.T) {
	cs := &countSender{}
	n := New("d", cs, Options{MinSeverity: Warning, PerMinute: 1, Burst: 2})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go n.Run(ctx)
	n.Enqueue(Message{Severity: Info, State: "firing"}) // below the floor
	for range 4 {
		n.Enqueue(Message{Severity: Critical, State: "firing"})
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && n.Status().Suppressed < 2 {
		time.Sleep(10 * time.Millisecond)
	}
	if cs.n.Load() != 2 || n.Status().Suppressed != 2 || n.Status().Sent != 2 {
		t.Fatalf("sent %d status %+v", cs.n.Load(), n.Status())
	}
	if Rank("bogus") != 0 || Rank(Critical) <= Rank(Warning) {
		t.Fatal("rank")
	}
}
