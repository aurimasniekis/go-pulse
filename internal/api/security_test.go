package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/aurimasniekis/go-pulse/internal/audit"
	"github.com/aurimasniekis/go-pulse/internal/clients"
	"github.com/aurimasniekis/go-pulse/internal/natsauth"
)

func TestSecurityAPIAndEnforcement(t *testing.T) {
	h := newHarness(t)
	base := h.admin.URL + AdminPrefix
	from := func(ip string) map[string]string { return map[string]string{"X-Test-IP": ip} }

	if resp, out := do(t, "POST", base+"/security/bans", `{"network":"203.0.113.5","duration_hours":1,"reason":"scanner"}`, csrfH); resp.StatusCode != http.StatusCreated || !strings.Contains(out, `"network":"203.0.113.5/32"`) {
		t.Fatalf("ban: %d %s", resp.StatusCode, out)
	}
	if resp, _ := do(t, "GET", h.public.URL+"/healthz", "", from("203.0.113.5")); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("banned client: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "GET", h.public.URL+"/healthz", "", from("203.0.113.6")); resp.StatusCode != http.StatusOK {
		t.Fatalf("other client: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "POST", base+"/security/bans", `{"network":"10.0.0.1","duration_hours":1,"reason":"x"}`, csrfH); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("never-ban network: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "POST", base+"/security/bans", `{"network":"garbage","reason":"x"}`, csrfH); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("garbage network: %d", resp.StatusCode)
	}

	// Token auth failures from one address lead to a ban.
	for range 5 {
		do(t, "POST", h.public.URL+PublicPrefix+"/publish/evt.a.b.c", "x", map[string]string{"X-Test-IP": "198.51.100.77", "Authorization": "Bearer pulse_aaaaaaaa_nope"})
	}
	if resp, _ := do(t, "GET", h.public.URL+"/healthz", "", from("198.51.100.77")); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("auto ban after token failures: %d", resp.StatusCode)
	}

	_, out := do(t, "GET", base+"/security", "", nil)
	var o SecurityOverview
	_ = json.Unmarshal([]byte(out), &o)
	if len(o.Active) != 2 || len(o.Failures) < 5 || o.Failures[0].Source != "http_token" || len(o.Offenders) != 1 {
		t.Fatalf("overview %s", out)
	}
	if resp, _ := do(t, "POST", base+"/security/bans/lift", `{"network":"203.0.113.5/32"}`, csrfH); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("lift: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "GET", h.public.URL+"/healthz", "", from("203.0.113.5")); resp.StatusCode != http.StatusOK {
		t.Fatalf("after lift: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "POST", base+"/security/allowlist", `{"network":"198.51.100.0/24","note":"office"}`, csrfH); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("allow: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "GET", h.public.URL+"/healthz", "", from("198.51.100.77")); resp.StatusCode != http.StatusOK {
		t.Fatalf("allowlisting did not lift the ban: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "POST", base+"/security/allowlist/remove", `{"network":"198.51.100.0/24"}`, csrfH); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("remove allow: %d", resp.StatusCode)
	}
}

// A browser-style NATS client connects through the public /ws proxy with its creds (PL-EDGE-005).
func TestWebSocketProxy(t *testing.T) {
	h := newHarness(t)
	_, creds, err := h.deps.Clients.Create(context.Background(), clients.CreateInput{Name: "browser", Grants: []natsauth.Grant{
		{Action: natsauth.ActPublish, Subject: "evt.ws.>"}, {Action: natsauth.ActSubscribe, Subject: "evt.ws.>"},
	}}, audit.System)
	if err != nil {
		t.Fatal(err)
	}
	path := t.TempDir() + "/b.creds"
	if err := writeFile(path, creds); err != nil {
		t.Fatal(err)
	}
	wsURL := strings.Replace(h.public.URL, "http://", "ws://", 1) + "/ws"
	nc, err := nats.Connect(wsURL, nats.UserCredentials(path), nats.Timeout(5*time.Second))
	if err != nil {
		t.Fatalf("connect over /ws: %v", err)
	}
	defer nc.Close()
	sub, err := nc.SubscribeSync("evt.ws.>")
	if err != nil {
		t.Fatal(err)
	}
	if err := nc.Publish("evt.ws.page.loaded", []byte("hi")); err != nil {
		t.Fatal(err)
	}
	if m, err := sub.NextMsg(3 * time.Second); err != nil || string(m.Data) != "hi" {
		t.Fatalf("round trip over /ws: %v", err)
	}
}

func writeFile(p string, b []byte) error { return os.WriteFile(p, b, 0o600) }
