package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

func (h *harness) token(t *testing.T, body string) string {
	t.Helper()
	resp, out := do(t, "POST", h.admin.URL+AdminPrefix+"/tokens", body, csrfH)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create token: %d %s", resp.StatusCode, out)
	}
	var r struct {
		Secret string `json:"secret"`
	}
	_ = json.Unmarshal([]byte(out), &r)
	return r.Secret
}

func bearer(tok string, extra map[string]string) map[string]string {
	m := map[string]string{"Authorization": "Bearer " + tok}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func TestIngest(t *testing.T) {
	h := newHarness(t)
	pub := h.public.URL + PublicPrefix
	tok := h.token(t, `{"name":"ci","grants":[{"action":"publish","subject":"evt.ing.>"},{"action":"subscribe","subject":"evt.ing.>"},{"action":"request","subject":"svc.*"}]}`)

	// Single publish with envelope headers and idempotency.
	hdr := bearer(tok, map[string]string{"Ce-Type": "ing.thing.v1", "Idempotency-Key": "k-1", "Content-Type": "application/json"})
	resp, out := do(t, "POST", pub+"/publish/evt.ing.thing.created", `{"a":1}`, hdr)
	if resp.StatusCode != http.StatusCreated || !strings.Contains(out, `"seq":1`) || !strings.Contains(out, `"id":"k-1"`) {
		t.Fatalf("publish: %d %s", resp.StatusCode, out)
	}
	if _, out := do(t, "POST", pub+"/publish/evt.ing.thing.created", `{"a":1}`, hdr); !strings.Contains(out, `"duplicate":true`) {
		t.Fatalf("dedupe: %s", out)
	}
	_, out = do(t, "GET", h.admin.URL+AdminPrefix+"/events/EVENTS/1", "", nil)
	if !strings.Contains(out, `"type":"ing.thing.v1"`) || !strings.Contains(out, `"source":"token:ci"`) || !strings.Contains(out, `"content_type":"application/json"`) {
		t.Fatalf("stored: %s", out)
	}

	// Authorization outcomes.
	before := authFailures.Load()
	resp, _ = do(t, "POST", pub+"/publish/evt.ing.thing.created", "x", nil)
	if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") == "" || authFailures.Load() != before+1 {
		t.Fatalf("no token: %d failures %d", resp.StatusCode, authFailures.Load()-before)
	}
	if resp, _ := do(t, "POST", pub+"/publish/evt.ing.thing.created", "x", bearer(tok+"x", nil)); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad token: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "POST", pub+"/publish/evt.other.thing.created", "x", bearer(tok, nil)); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("outside grant: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "POST", pub+"/publish/evt.ing.bad", "x", bearer(tok, nil)); resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("bad subject: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "POST", pub+"/publish/evt.ing.x.y", strings.Repeat("a", 1<<20+10), bearer(tok, nil)); resp.StatusCode != http.StatusRequestEntityTooLarge && resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("oversized body: %d", resp.StatusCode)
	}

	// Batch: mixed payload kinds; one forbidden event fails the whole batch before anything is published.
	batch := `{"events":[{"subject":"evt.ing.b.json","type":"b","data":{"k":1}},{"subject":"evt.ing.b.text","data":"hello"},{"subject":"evt.ing.b.bin","data_base64":"AAE="}]}`
	resp, out = do(t, "POST", pub+"/publish", batch, bearer(tok, nil))
	if resp.StatusCode != http.StatusOK || strings.Count(out, `"seq"`) != 3 {
		t.Fatalf("batch: %d %s", resp.StatusCode, out)
	}
	_, last := do(t, "GET", h.admin.URL+AdminPrefix+"/events?limit=1", "", nil)
	if resp, _ := do(t, "POST", pub+"/publish", `{"events":[{"subject":"evt.ing.ok.x"},{"subject":"evt.nope.x.y"}]}`, bearer(tok, nil)); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("partly forbidden batch: %d", resp.StatusCode)
	}
	if _, now := do(t, "GET", h.admin.URL+AdminPrefix+"/events?limit=1", "", nil); now != last {
		t.Fatal("a forbidden batch published something")
	}
	_, out = do(t, "GET", h.admin.URL+AdminPrefix+"/events?subject=evt.ing.b.>&dir=forward", "", nil)
	if !strings.Contains(out, `"content_type":"application/json"`) || !strings.Contains(out, `"data":"hello"`) || !strings.Contains(out, `"encoding":"base64"`) {
		t.Fatalf("batch payloads: %s", out)
	}

	// Request bridge.
	sub, err := h.app.Conn().Subscribe("svc.echo", func(m *nats.Msg) {
		r := nats.NewMsg(m.Reply)
		r.Data = append([]byte("echo:"), m.Data...)
		r.Header.Set("content-type", "text/plain")
		_ = m.RespondMsg(r)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Unsubscribe() }()
	_ = h.app.Conn().Flush()
	resp, out = do(t, "POST", pub+"/request/svc.echo", "ping", bearer(tok, nil))
	if resp.StatusCode != http.StatusOK || out != "echo:ping" || resp.Header.Get("Content-Type") != "text/plain" {
		t.Fatalf("request: %d %q %s", resp.StatusCode, out, resp.Header.Get("Content-Type"))
	}
	if resp, _ := do(t, "POST", pub+"/request/svc.none?timeout_ms=300", "ping", bearer(tok, nil)); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("no responders: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "POST", pub+"/request/other.x", "ping", bearer(tok, nil)); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("request outside grant: %d", resp.StatusCode)
	}

	// Replay.
	resp, out = do(t, "GET", pub+"/events?subject=evt.ing.>&limit=2", "", bearer(tok, nil))
	if resp.StatusCode != http.StatusOK || !strings.Contains(out, `"seq":1`) || !strings.Contains(out, `"next"`) {
		t.Fatalf("replay: %d %s", resp.StatusCode, out)
	}
	if resp, _ := do(t, "GET", pub+"/events?subject=evt.>", "", bearer(tok, nil)); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("replay wider than grant: %d", resp.StatusCode)
	}
}

func TestPublicTailAuth(t *testing.T) {
	h := newHarness(t)
	tok := h.token(t, `{"name":"tail","grants":[{"action":"subscribe","subject":"evt.test.>"}]}`)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	get := func(hdr map[string]string, q string) *http.Response {
		req, _ := http.NewRequestWithContext(ctx, "GET", h.public.URL+PublicPrefix+"/tail"+q, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = resp.Body.Close() })
		return resp
	}
	if r := get(nil, "?subject=evt.test.>"); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("tail without token: %d", r.StatusCode)
	}
	if r := get(bearer(tok, nil), "?subject=evt.>"); r.StatusCode != http.StatusForbidden {
		t.Fatalf("tail wider than grant: %d", r.StatusCode)
	}
	r := get(bearer(tok, map[string]string{"Last-Event-ID": "0"}), "?subject=evt.test.>")
	if r.StatusCode != http.StatusOK {
		t.Fatalf("tail: %d", r.StatusCode)
	}
	time.Sleep(200 * time.Millisecond)
	h.publish(t, 1)
	if got := readSSE(t, bufio.NewReader(r.Body), 1); got[0].event != "event" {
		t.Fatalf("%+v", got)
	}
}

func TestTokenCIDR(t *testing.T) {
	h := newHarness(t)
	tok := h.token(t, `{"name":"lan","cidrs":["10.0.0.0/8"],"grants":[{"action":"publish","subject":"evt.ing.>"}]}`)
	if resp, _ := do(t, "POST", h.public.URL+PublicPrefix+"/publish/evt.ing.a.b", "x", bearer(tok, nil)); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("token used outside its CIDR: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "POST", h.admin.URL+AdminPrefix+"/tokens", `{"name":"bad","grants":[{"action":"durable","subject":"evt.>","name":"x"}]}`, csrfH); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("durable grant on a token: %d", resp.StatusCode)
	}
}

func TestClientsAndAuditAPI(t *testing.T) {
	h := newHarness(t)
	base := h.admin.URL + AdminPrefix
	resp, out := do(t, "POST", base+"/clients", `{"name":"bot","grants":[{"action":"publish","subject":"evt.bot.>"}],"expires_in_days":10}`, csrfH)
	if resp.StatusCode != http.StatusCreated || !strings.Contains(out, "NATS USER JWT") || !strings.Contains(out, `"expires_soon":true`) {
		t.Fatalf("create: %d %s", resp.StatusCode, out)
	}
	var issued struct {
		Client struct {
			ID      string `json:"id"`
			Version int64  `json:"version"`
		} `json:"client"`
	}
	_ = json.Unmarshal([]byte(out), &issued)
	id := issued.Client.ID
	if _, out := do(t, "GET", base+"/clients", "", nil); !strings.Contains(out, `"name":"bot"`) || strings.Contains(out, "USER NKEY SEED") {
		t.Fatalf("list: %s", out)
	}
	grants := `{"grants":[{"action":"subscribe","subject":"evt.bot.>"}],"version":` + jsonInt(issued.Client.Version) + `}`
	if resp, out := do(t, "PUT", base+"/clients/"+id+"/grants", grants, csrfH); resp.StatusCode != http.StatusOK || !strings.Contains(out, `"action":"subscribe"`) {
		t.Fatalf("grants: %d %s", resp.StatusCode, out)
	}
	if resp, _ := do(t, "PUT", base+"/clients/"+id+"/grants", grants, csrfH); resp.StatusCode != http.StatusConflict {
		t.Fatalf("stale version: %d", resp.StatusCode)
	}
	if resp, out := do(t, "POST", base+"/clients/"+id+"/rotate", `{}`, csrfH); resp.StatusCode != http.StatusOK || !strings.Contains(out, "USER NKEY SEED") {
		t.Fatalf("rotate: %d", resp.StatusCode)
	}
	if resp, out := do(t, "GET", base+"/clients/"+id+"/connections", "", nil); resp.StatusCode != http.StatusOK || out != "[]\n" && out != "[]" {
		t.Fatalf("connections: %d %q", resp.StatusCode, out)
	}
	if resp, _ := do(t, "POST", base+"/clients/"+id+"/kick", `{}`, csrfH); resp.StatusCode != http.StatusOK {
		t.Fatalf("kick: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "POST", base+"/clients/"+id+"/revoke", `{}`, csrfH); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke: %d", resp.StatusCode)
	}
	if _, out := do(t, "GET", base+"/clients/"+id, "", nil); !strings.Contains(out, `"status":"revoked"`) {
		t.Fatalf("after revoke: %s", out)
	}
	if resp, _ := do(t, "GET", base+"/clients/zzzzzzzz", "", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing: %d", resp.StatusCode)
	}

	_, out = do(t, "GET", base+"/audit?action=client.&limit=2", "", nil)
	var page AuditPage
	_ = json.Unmarshal([]byte(out), &page)
	if len(page.Entries) != 2 || page.Entries[0].Action != "client.revoke" || page.Next == 0 {
		t.Fatalf("audit page: %s", out)
	}
	_, out = do(t, "GET", base+"/audit?action=client.&before_id="+jsonInt(page.Next), "", nil)
	if !strings.Contains(out, `"client.create"`) || strings.Contains(out, "SEED") {
		t.Fatalf("audit page 2: %s", out)
	}
}

func jsonInt(n int64) string { b, _ := json.Marshal(n); return string(b) }
