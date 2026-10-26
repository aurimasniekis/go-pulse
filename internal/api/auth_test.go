package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"github.com/aurimasniekis/go-pulse/internal/audit"
	"github.com/aurimasniekis/go-pulse/internal/rbac"
	"github.com/aurimasniekis/go-pulse/internal/users"
)

// Every admin operation has an authorization rule, and the table has no stale entries (ADR-0038).
func TestEveryOperationHasARule(t *testing.T) {
	api := NewAdminAPI(http.NewServeMux(), &Deps{})
	seen := map[string]bool{}
	for path, item := range api.OpenAPI().Paths {
		for _, op := range []*huma.Operation{item.Get, item.Post, item.Put, item.Patch, item.Delete} {
			if op == nil {
				continue
			}
			seen[op.OperationID] = true
			r, ok := opRules[op.OperationID]
			if !ok {
				t.Errorf("%s %s (%s) has no rule in opRules", op.Method, path, op.OperationID)
				continue
			}
			if !rbac.Known(r.perm) {
				t.Errorf("%s: unknown permission %q", op.OperationID, r.perm)
			}
			if r.perm == rbac.Public && op.Method == http.MethodGet && op.OperationID != "get-auth-config" {
				t.Errorf("%s: only sign-in may be public", op.OperationID)
			}
		}
	}
	for id := range opRules {
		if !seen[id] {
			t.Errorf("opRules has %s, which is not an operation", id)
		}
	}
}

type client struct {
	t            *testing.T
	base, cookie string
	csrf         string
}

func (c *client) do(method, path, body string) (int, string) {
	c.t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), method, c.base+path, strings.NewReader(body))
	req.Header.Set("X-Test-Anonymous", "1")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", strings.TrimSuffix(c.base, AdminPrefix))
	if c.cookie != "" {
		req.AddCookie(&http.Cookie{Name: "pulse_session", Value: c.cookie})
		req.Header.Set(CSRFHeader, c.csrf)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// signInAs creates a user with roles and a password and signs in on the loopback admin listener.
func (h *harness) signInAs(t *testing.T, email string, roles ...string) *client {
	t.Helper()
	ctx := context.Background()
	u, err := h.deps.Users.Create(ctx, audit.System, users.CreateInput{Email: email, DisplayName: email, Roles: roles})
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := h.deps.Users.IssueResetToken(ctx, audit.System, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.deps.Users.ResetPassword(ctx, tok, testAdminPassword, ""); err != nil {
		t.Fatal(err)
	}
	c := &client{t: t, base: h.admin.URL + AdminPrefix}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/auth/login",
		strings.NewReader(`{"email":"`+email+`","password":"`+testAdminPassword+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", h.admin.URL)
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Me Me `json:"me"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("login %s: %d %v", email, resp.StatusCode, err)
	}
	for _, ck := range resp.Cookies() {
		if ck.Name == "pulse_session" {
			c.cookie = ck.Value
		}
	}
	c.csrf = out.Me.CSRFToken
	return c
}

// Roles decide what the API allows (PL-USR-003).
func TestRolesThroughTheAPI(t *testing.T) {
	h := newHarness(t)
	viewer := h.signInAs(t, "viewer@example.com", users.RoleViewer)
	auditor := h.signInAs(t, "auditor@example.com", users.RoleAuditor)
	operator := h.signInAs(t, "operator@example.com", users.RoleOperator)
	nobody := h.signInAs(t, "nobody@example.com")
	stream := `{"name":"ORDERS","subjects":["orders.>"]}`
	for _, c := range []struct {
		who          *client
		method, path string
		body         string
		want         int
	}{
		{viewer, "GET", "/overview", "", 200},
		{viewer, "GET", "/events", "", 200},
		{viewer, "GET", "/audit", "", 403},
		{viewer, "GET", "/users", "", 403},
		{viewer, "POST", "/streams", stream, 403},
		{auditor, "GET", "/audit", "", 200},
		{auditor, "GET", "/users", "", 200},
		{auditor, "POST", "/streams", stream, 403},
		{operator, "POST", "/streams", stream, 201},
		{operator, "GET", "/users", "", 403},
		{operator, "POST", "/invitations", `{"email":"x@example.com","roles":["viewer"]}`, 403},
		{nobody, "GET", "/overview", "", 403},
		{nobody, "GET", "/auth/me", "", 200},
	} {
		if got, body := c.who.do(c.method, c.path, c.body); got != c.want {
			t.Errorf("%s %s: %d, want %d (%s)", c.method, c.path, got, c.want, body)
		}
	}
	_, me := viewer.do("GET", "/auth/me", "")
	if !strings.Contains(me, `"events.read"`) || strings.Contains(me, `"users.manage"`) {
		t.Fatalf("viewer permissions: %s", me)
	}
}

// Personal access tokens work on the loopback listener, and on the public host only from their networks when enabled,
// without CSRF or Origin; the admin listener refuses sign-in attempts from other origins.
func TestPersonalAccessTokens(t *testing.T) {
	h := newHarness(t)
	base := h.admin.URL + AdminPrefix
	resp, body := do(t, "POST", base+"/account/tokens", `{"name":"laptop cli","role":"administrator","days":30}`, csrfH)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	var out struct {
		Secret string `json:"secret"`
	}
	_ = json.Unmarshal([]byte(body), &out)
	bearer := map[string]string{"X-Test-Anonymous": "1", "Authorization": "Bearer " + out.Secret}
	if resp, body := do(t, "GET", base+"/clients", "", bearer); resp.StatusCode != http.StatusOK {
		t.Fatalf("token read: %d %s", resp.StatusCode, body)
	}
	if resp, body := do(t, "POST", base+"/streams", `{"name":"CLI","subjects":["cli.>"]}`, bearer); resp.StatusCode != http.StatusCreated {
		t.Fatalf("token write without CSRF: %d %s", resp.StatusCode, body)
	}
	if resp, _ := do(t, "POST", base+"/account/tokens", `{"name":"again","role":"administrator"}`, bearer); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("token created a token: %d", resp.StatusCode)
	}
	// The public dashboard host accepts no tokens unless AdminPublicPATs is on.
	h.deps.AdminPublicHost, h.deps.PublicAPIHost = "admin.events.example.com", "events.example.com"
	pub := httptest.NewServer(NewPublic(h.deps))
	defer pub.Close()
	hdr := map[string]string{"Host": "admin.events.example.com", "X-Test-Anonymous": "1", "Authorization": "Bearer " + out.Secret}
	if resp, _ := do(t, "GET", pub.URL+AdminPrefix+"/clients", "", hdr); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("token on the public listener: %d", resp.StatusCode)
	}
	// With it on, a token works there only from its own networks (ADR-0041), and a refused token feeds the ban detector.
	resp, body = do(t, "POST", base+"/account/tokens", `{"name":"terraform","role":"administrator","cidrs":["203.0.113.7"]}`, csrfH)
	if resp.StatusCode != http.StatusCreated || !strings.Contains(body, `"cidrs":["203.0.113.7/32"]`) {
		t.Fatalf("create with networks: %d %s", resp.StatusCode, body)
	}
	var ci struct {
		Secret string `json:"secret"`
	}
	_ = json.Unmarshal([]byte(body), &ci)
	if resp, _ := do(t, "POST", base+"/account/tokens", `{"name":"wide","role":"administrator","cidrs":["0.0.0.0/0"]}`, csrfH); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("token for every network: %d", resp.StatusCode)
	}
	h.deps.AdminPublicPATs = true
	pats := httptest.NewServer(NewPublic(h.deps))
	defer pats.Close()
	from := func(secret, ip string) map[string]string {
		return map[string]string{"Host": "admin.events.example.com", "X-Test-Anonymous": "1", "X-Test-IP": ip, "Authorization": "Bearer " + secret}
	}
	if resp, body := do(t, "GET", pats.URL+AdminPrefix+"/clients", "", from(ci.Secret, "203.0.113.7")); resp.StatusCode != http.StatusOK {
		t.Fatalf("token from its network: %d %s", resp.StatusCode, body)
	}
	if resp, body := do(t, "POST", pats.URL+AdminPrefix+"/streams", `{"name":"TF","subjects":["tf.>"]}`, from(ci.Secret, "203.0.113.7")); resp.StatusCode != http.StatusCreated {
		t.Fatalf("token write on the public host: %d %s", resp.StatusCode, body)
	}
	before := authFailures.Load()
	for _, c := range []struct{ secret, ip string }{{ci.Secret, "198.51.100.9"}, {out.Secret, "203.0.113.7"}} {
		if resp, _ := do(t, "GET", pats.URL+AdminPrefix+"/clients", "", from(c.secret, c.ip)); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("token from %s: %d", c.ip, resp.StatusCode)
		}
	}
	if authFailures.Load() != before+2 {
		t.Fatal("refused public tokens not reported to the ban detector")
	}
	if resp, _ := do(t, "GET", base+"/clients", "", map[string]string{"X-Test-Anonymous": "1", "Authorization": "Bearer pulse_pat_nope"}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad token: %d", resp.StatusCode)
	}
	// Sign-in from a foreign origin is refused before any password check.
	login := `{"email":"` + testAdminEmail + `","password":"` + testAdminPassword + `"}`
	if resp, _ := do(t, "POST", base+"/auth/login", login, map[string]string{"X-Test-Anonymous": "1", "Origin": "https://evil.example"}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site login: %d", resp.StatusCode)
	}
}

// The dashboard on the public listener: its own hostname, or the API hostname for non-API paths (ADR-0038).
func TestPublicDashboardRouting(t *testing.T) {
	h := newHarness(t)
	h.deps.AdminPublicHost, h.deps.PublicAPIHost = "admin.events.example.com", "events.example.com"
	sep := httptest.NewServer(NewPublic(h.deps))
	defer sep.Close()
	anon := func(host string) map[string]string { return map[string]string{"Host": host, "X-Test-Anonymous": "1"} }
	for _, c := range []struct {
		host, path string
		want       int
	}{
		{"admin.events.example.com", AdminPrefix + "/auth/config", 200},
		{"admin.events.example.com", AdminPrefix + "/overview", 401},
		{"events.example.com", AdminPrefix + "/auth/config", 404},
		{"events.example.com", "/healthz", 200},
		{"other.example.com", "/healthz", 421},
	} {
		if resp, body := do(t, "GET", sep.URL+c.path, "", anon(c.host)); resp.StatusCode != c.want {
			t.Errorf("separate %s%s: %d, want %d %s", c.host, c.path, resp.StatusCode, c.want, body)
		}
	}
	h.deps.AdminPublicHost = "events.example.com"
	same := httptest.NewServer(NewPublic(h.deps))
	defer same.Close()
	for _, c := range []struct {
		path string
		want int
	}{
		{AdminPrefix + "/auth/config", 200},
		{"/healthz", 200},
		{PublicPrefix + "/health", 200},
	} {
		if resp, body := do(t, "GET", same.URL+c.path, "", anon("events.example.com")); resp.StatusCode != c.want {
			t.Errorf("same host %s: %d, want %d %s", c.path, resp.StatusCode, c.want, body)
		}
	}
	// A failed password sign-in on the public listener feeds the ban detector.
	before := authFailures.Load()
	hdr := anon("events.example.com")
	hdr["Origin"] = "http://events.example.com"
	if resp, _ := do(t, "POST", same.URL+AdminPrefix+"/auth/login", `{"email":"x@example.com","password":"wrong password!"}`, hdr); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad login: %d", resp.StatusCode)
	}
	if authFailures.Load() != before+1 {
		t.Fatal("failed public sign-in not reported to the ban detector")
	}
}
