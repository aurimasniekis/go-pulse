package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWebhooksAPI(t *testing.T) {
	h := newHarness(t)
	base := h.admin.URL + AdminPrefix

	// Inbound: create, then deliver through the public listener.
	resp, out := do(t, "POST", base+"/hooks/inbound", `{"name":"gh","kind":"github","enabled":true}`, csrfH)
	if resp.StatusCode != http.StatusCreated || !strings.Contains(out, `"url":"https://events.example.test/hooks/gh"`) {
		t.Fatalf("create inbound: %d %s", resp.StatusCode, out)
	}
	var in struct {
		Hook struct {
			ID string `json:"id"`
		} `json:"hook"`
		Secret string `json:"secret"`
	}
	_ = json.Unmarshal([]byte(out), &in)
	body := `{"zen":"Keep it simple"}`
	m := hmac.New(sha256.New, []byte(in.Secret))
	m.Write([]byte(body))
	sig := "sha256=" + hex.EncodeToString(m.Sum(nil))
	if resp, _ := do(t, "POST", h.public.URL+"/hooks/gh", body, map[string]string{"X-Hub-Signature-256": sig, "X-GitHub-Event": "ping"}); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("public delivery: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "POST", h.public.URL+"/hooks/gh", body, map[string]string{"X-Hub-Signature-256": "sha256=00"}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad signature: %d", resp.StatusCode)
	}
	if _, out := do(t, "GET", base+"/events/EVENTS/1", "", nil); !strings.Contains(out, `"type":"github.ping"`) {
		t.Fatalf("hook event: %s", out)
	}
	resp, out = do(t, "PUT", base+"/hooks/inbound/"+in.Hook.ID, `{"name":"gh","kind":"github","enabled":true,"rotate_secret":true}`, csrfH)
	if resp.StatusCode != http.StatusOK || !strings.Contains(out, `"secret":"`) {
		t.Fatalf("rotate: %d %s", resp.StatusCode, out)
	}
	if resp, _ := do(t, "DELETE", base+"/hooks/inbound/"+in.Hook.ID, "", csrfH); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete inbound: %d", resp.StatusCode)
	}

	// Outbound: create against a local receiver, deliver, test, pause, retry.
	got := make(chan string, 10)
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- r.Header.Get("Pulse-Event-Type") + "|" + string(b)
	}))
	defer recv.Close()
	resp, out = do(t, "POST", base+"/hooks/outbound", `{"name":"recv","filter":"evt.test.>","url":"`+recv.URL+`"}`, csrfH)
	if resp.StatusCode != http.StatusCreated || !strings.Contains(out, `"secret":"whsec_`) {
		t.Fatalf("create outbound: %d %s", resp.StatusCode, out)
	}
	var ob struct {
		Hook struct {
			ID string `json:"id"`
		} `json:"hook"`
	}
	_ = json.Unmarshal([]byte(out), &ob)
	h.publish(t, 1)
	select {
	case v := <-got:
		if !strings.HasPrefix(v, "test.thing.created.v1|") {
			t.Fatalf("delivered %q", v)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no delivery")
	}
	if resp, out := do(t, "POST", base+"/hooks/outbound/"+ob.Hook.ID+"/test", `{}`, csrfH); resp.StatusCode != http.StatusOK || !strings.Contains(out, `"outcome":"test"`) {
		t.Fatalf("test send: %d %s", resp.StatusCode, out)
	}
	<-got
	if _, out := do(t, "POST", base+"/hooks/outbound/"+ob.Hook.ID+"/pause", `{}`, csrfH); !strings.Contains(out, `"paused":true`) {
		t.Fatalf("pause: %s", out)
	}
	_, out = do(t, "GET", base+"/hooks/outbound/"+ob.Hook.ID+"/deliveries", "", nil)
	var ds []struct {
		ID      int64  `json:"id"`
		Outcome string `json:"outcome"`
	}
	_ = json.Unmarshal([]byte(out), &ds)
	if len(ds) != 2 || ds[0].Outcome != "test" || ds[1].Outcome != "delivered" {
		t.Fatalf("deliveries %s", out)
	}
	if resp, out := do(t, "POST", base+"/hooks/deliveries/"+jsonInt(ds[1].ID)+"/retry", `{}`, csrfH); resp.StatusCode != http.StatusOK || !strings.Contains(out, `"outcome":"manual"`) {
		t.Fatalf("retry: %d %s", resp.StatusCode, out)
	}
	<-got
	if resp, _ := do(t, "POST", base+"/hooks/outbound", `{"name":"bad","filter":"evt.>","url":"http://169.254.169.254/"}`, csrfH); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("metadata url: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "DELETE", base+"/hooks/outbound/"+ob.Hook.ID, "", csrfH); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete outbound: %d", resp.StatusCode)
	}
}
