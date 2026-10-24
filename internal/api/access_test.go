package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func auditCount(t *testing.T, h *harness, action, target string) int {
	t.Helper()
	var n int
	if err := h.db.R().QueryRow("SELECT COUNT(*) FROM audit WHERE action = ? AND target = ? AND result = 'ok'", action, target).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestDeleteClient: a client record can be deleted only once revoked; the revocation stays in force and the name
// becomes reusable (declarative tooling destroys and re-creates by name).
func TestDeleteClient(t *testing.T) {
	h := newHarness(t)
	base := h.admin.URL + AdminPrefix
	create := func() Issued {
		t.Helper()
		resp, body := do(t, http.MethodPost, base+"/clients", `{"name":"deployer","grants":[{"action":"publish","subject":"evt.deploy.>"}]}`, csrfH)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create: %d %s", resp.StatusCode, body)
		}
		var iss Issued
		if err := json.Unmarshal([]byte(body), &iss); err != nil {
			t.Fatal(err)
		}
		return iss
	}
	first := create().Client
	id := first.ID

	resp, body := do(t, http.MethodDelete, base+"/clients/"+id, "", csrfH)
	if resp.StatusCode != http.StatusPreconditionFailed || !strings.Contains(body, `"code":"precondition"`) || !strings.Contains(body, "revoke the client first") {
		t.Fatalf("delete active client: %d %s", resp.StatusCode, body)
	}
	if resp, body := do(t, http.MethodPost, base+"/clients", `{"name":"deployer"}`, csrfH); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("duplicate name while the client exists: %d %s", resp.StatusCode, body)
	}
	if resp, body := do(t, http.MethodPost, base+"/clients/"+id+"/revoke", "", csrfH); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", resp.StatusCode, body)
	}
	if resp, body := do(t, http.MethodDelete, base+"/clients/"+id, "", csrfH); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete revoked client: %d %s", resp.StatusCode, body)
	}
	if resp, _ := do(t, http.MethodGet, base+"/clients/"+id, "", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get deleted client: %d", resp.StatusCode)
	}
	if resp, _ := do(t, http.MethodDelete, base+"/clients/"+id, "", csrfH); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("delete twice: %d", resp.StatusCode)
	}
	if resp, _ := do(t, http.MethodDelete, base+"/clients/zzzzzzzzzz", "", csrfH); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("delete unknown: %d", resp.StatusCode)
	}

	// The revocation outlives the record and still reaches the APP account.
	var n int
	if err := h.db.R().QueryRow("SELECT COUNT(*) FROM client_revocations WHERE client_id = ? AND user_key = ?", id, first.UserKey).Scan(&n); err != nil || n != 1 {
		t.Fatalf("revocation rows %d %v", n, err)
	}
	spec, err := h.deps.Clients.AppAccountSpec(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	revoked := false
	for _, r := range spec.Revocations {
		revoked = revoked || r.UserKey == first.UserKey
	}
	if !revoked {
		t.Fatalf("user key %s missing from the APP account revocations", first.UserKey)
	}
	if auditCount(t, h, "client.delete", id) != 1 {
		t.Fatal("no client.delete audit entry")
	}

	// The name is free again.
	second := create().Client
	if second.ID == id || second.Name != "deployer" || second.Status != "active" {
		t.Fatalf("re-created client %+v", second)
	}
}

// TestDeleteToken: only revoked or expired tokens can be deleted; the name becomes reusable.
func TestDeleteToken(t *testing.T) {
	h := newHarness(t)
	base := h.admin.URL + AdminPrefix
	create := func(name string) string {
		t.Helper()
		resp, body := do(t, http.MethodPost, base+"/tokens", `{"name":"`+name+`","grants":[{"action":"publish","subject":"evt.ci.>"}]}`, csrfH)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create token: %d %s", resp.StatusCode, body)
		}
		var out struct {
			Token struct {
				ID string `json:"id"`
			} `json:"token"`
		}
		_ = json.Unmarshal([]byte(body), &out)
		return out.Token.ID
	}
	id := create("ci")
	resp, body := do(t, http.MethodDelete, base+"/tokens/"+id, "", csrfH)
	if resp.StatusCode != http.StatusPreconditionFailed || !strings.Contains(body, `"code":"precondition"`) || !strings.Contains(body, "revoke the token first") {
		t.Fatalf("delete active token: %d %s", resp.StatusCode, body)
	}
	if resp, _ := do(t, http.MethodPost, base+"/tokens/"+id+"/revoke", "", csrfH); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke: %d", resp.StatusCode)
	}
	if resp, body := do(t, http.MethodDelete, base+"/tokens/"+id, "", csrfH); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete revoked token: %d %s", resp.StatusCode, body)
	}
	if _, body := do(t, http.MethodGet, base+"/tokens", "", nil); strings.Contains(body, id) {
		t.Fatalf("deleted token listed: %s", body)
	}
	if resp, _ := do(t, http.MethodDelete, base+"/tokens/"+id, "", csrfH); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("delete twice: %d", resp.StatusCode)
	}
	if auditCount(t, h, "token.delete", id) != 1 {
		t.Fatal("no token.delete audit entry")
	}

	// Same name again; an expired (not revoked) token can be deleted too.
	id2 := create("ci")
	if id2 == id {
		t.Fatal("token id reused")
	}
	if err := h.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), "UPDATE http_tokens SET expires_at = 1 WHERE id = ?", id2)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if resp, body := do(t, http.MethodDelete, base+"/tokens/"+id2, "", csrfH); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete expired token: %d %s", resp.StatusCode, body)
	}
}
