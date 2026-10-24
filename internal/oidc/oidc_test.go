package oidc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aurimasniekis/go-pulse/internal/audit"
	"github.com/aurimasniekis/go-pulse/internal/oidc/oidctest"
	"github.com/aurimasniekis/go-pulse/internal/store"
	"github.com/aurimasniekis/go-pulse/internal/users"
)

var admin = audit.Actor{Name: "user:root@example.com"}

func ptr[T any](v T) *T { return &v }

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

type fx struct {
	t    *testing.T
	ctx  context.Context
	clk  *clock
	db   *store.DB
	auth *users.Service
	svc  *Service
	idp  *oidctest.IdP
	prov *Provider
	meta users.SessionMeta
}

func newFx(t *testing.T, mods ...func(*ProviderInput)) *fx {
	t.Helper()
	ctx := context.Background()
	clk := &clock{now: time.Now().UTC().Truncate(time.Millisecond)}
	db, err := store.Open(ctx, store.Options{Path: filepath.Join(t.TempDir(), "pulse.db")})
	must(t, err)
	t.Cleanup(func() { _ = db.Close() })
	keys, err := users.DeriveKeys(bytes.Repeat([]byte{7}, 32))
	must(t, err)
	auth := users.New(db, keys, users.Options{Now: clk.Now, MFARequiredRoles: []string{}})
	svc := New(auth, Options{BaseURL: "https://admin.events.example.com", JWKSMinRefetch: 10 * time.Second})
	idp := oidctest.New(t)
	idp.Now = clk.Now
	idp.RedirectURI = "https://admin.events.example.com/auth/oidc/callback"
	in := ProviderInput{Slug: ptr("corp"), DisplayName: ptr("Corp SSO"), Issuer: ptr(idp.Issuer()), ClientID: ptr(idp.ClientID),
		ClientSecret: ptr(idp.ClientSecret), CABundlePEM: ptr(idp.CAPEM()), AllowPrivateNetwork: ptr(true), Enabled: ptr(true), GroupsClaim: ptr("groups")}
	for _, m := range mods {
		m(&in)
	}
	p, err := svc.CreateProvider(ctx, admin, in)
	must(t, err)
	return &fx{t: t, ctx: ctx, clk: clk, db: db, auth: auth, svc: svc, idp: idp, prov: p,
		meta: users.SessionMeta{IP: "198.51.100.7", Listener: "public"}}
}

// flow runs Start → IdP authorize → Callback.
func (f *fx) flow(purpose string, p *users.Principal) (*CallbackResult, error) {
	f.t.Helper()
	st, err := f.svc.Start(f.ctx, StartInput{Provider: f.prov.Slug, Purpose: purpose, ReturnTo: "/explorer?x=1", Principal: p})
	if err != nil {
		return nil, err
	}
	q, err := f.idp.Authorize(st.RedirectURL)
	must(f.t, err)
	return f.svc.Callback(f.ctx, CallbackInput{State: q.Get("state"), Code: q.Get("code"), Error: q.Get("error"), Binding: st.Binding, Principal: p, Meta: f.meta})
}

const testPassword = "correct horse battery staple"

// user creates a user with a password (through a reset link, the only way passwords are set).
func (f *fx) user(email string, roles ...string) *users.User {
	f.t.Helper()
	u, err := f.auth.Create(f.ctx, admin, users.CreateInput{Email: email, DisplayName: email, Roles: roles})
	must(f.t, err)
	tok, _, err := f.auth.IssueResetToken(f.ctx, admin, u.ID)
	must(f.t, err)
	must(f.t, f.auth.ResetPassword(f.ctx, tok, testPassword, ""))
	return u
}

func (f *fx) principal(token string) *users.Principal {
	f.t.Helper()
	p, err := f.auth.Authenticate(f.ctx, token, f.meta.Listener)
	must(f.t, err)
	return p
}

func kindOf(err error, k users.Kind) bool { return users.KindOf(err) == k }

// Code flow with PKCE, a validated ID token, identity by (iss, sub), session created (PL-USR-008).
func TestLoginPreprovisioned(t *testing.T) {
	f := newFx(t)
	u := f.user("alice@corp.example", users.RoleOperator)
	if _, err := f.flow(PurposeLogin, nil); !errors.Is(err, ErrNotProvisioned) {
		t.Fatalf("unlinked identity: %v", err)
	}
	must(t, f.svc.LinkIdentity(f.ctx, admin, u.ID, f.prov.ID, "user-1"))
	res, err := f.flow(PurposeLogin, nil)
	must(t, err)
	if res.ReturnTo != "/explorer?x=1" || res.UserID != u.ID {
		t.Fatalf("%+v", res)
	}
	p := f.principal(res.SessionToken)
	if p.Method != users.MethodOIDC || p.Provider != "corp" || !p.HasRole(users.RoleOperator) || p.Limited {
		t.Fatalf("principal %+v", p)
	}
	q := f.idp.LastAuthorizeParams()
	if q.Get("code_challenge_method") != "S256" || q.Get("nonce") == "" || q.Get("redirect_uri") != "https://admin.events.example.com/auth/oidc/callback" ||
		q.Get("prompt") != "" {
		t.Fatalf("authorize params %v", q)
	}
	ids, err := f.svc.Identities(f.ctx, u.ID)
	if err != nil || len(ids) != 1 || ids[0].LastLoginAt == nil {
		t.Fatalf("%+v %v", ids, err)
	}
	// A session is bound to its listener.
	if _, err := f.auth.Authenticate(f.ctx, res.SessionToken, "loopback"); err == nil {
		t.Fatal("public session accepted on the loopback listener")
	}
}

// An email match never links or merges accounts.
func TestNoEmailMerge(t *testing.T) {
	f := newFx(t, func(in *ProviderInput) {
		in.Provisioning = ptr(JIT)
		in.JIT = &JITRules{EmailDomains: []string{"example.com"}}
	})
	f.user("alice@example.com", users.RoleAdministrator) // same email as the IdP user
	if _, err := f.flow(PurposeLogin, nil); !kindOf(err, users.KindConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
	var n int
	must(t, f.db.R().QueryRowContext(f.ctx, `SELECT count(*) FROM user_identities`).Scan(&n))
	if n != 0 {
		t.Fatal("identity linked by email")
	}
}

// JIT restrictions, no role without a mapping, role sync on every login, manual roles kept (PL-USR-009).
func TestJITAndRoleSync(t *testing.T) {
	f := newFx(t, func(in *ProviderInput) {
		in.Provisioning = ptr(JIT)
		in.JIT = &JITRules{EmailDomains: []string{"example.com"}, Groups: []string{"pulse-users"}}
	})
	f.user("root@corp.example", users.RoleAdministrator) // keeps role changes clear of the last-admin guard
	f.idp.Set(func(i *oidctest.IdP) { i.User.Claims["groups"] = []string{"other"} })
	if _, err := f.flow(PurposeLogin, nil); !errors.Is(err, ErrNotProvisioned) {
		t.Fatalf("group restriction: %v", err)
	}
	f.idp.Set(func(i *oidctest.IdP) {
		i.User.Claims["groups"] = []string{"pulse-users"}
		i.User.Claims["email_verified"] = false
	})
	if _, err := f.flow(PurposeLogin, nil); !errors.Is(err, ErrNotProvisioned) {
		t.Fatalf("unverified email: %v", err)
	}
	f.idp.Set(func(i *oidctest.IdP) { i.User.Claims["email_verified"] = true })
	res, err := f.flow(PurposeLogin, nil)
	must(t, err)
	p := f.principal(res.SessionToken)
	if len(p.User.Roles) != 0 || p.User.ProvisionedVia != "jit" {
		t.Fatalf("JIT user got roles %v via %s", p.User.Roles, p.User.ProvisionedVia)
	}
	_, err = f.svc.AddMapping(f.ctx, admin, f.prov.ID, "pulse-operators", users.RoleOperator)
	must(t, err)
	f.idp.Set(func(i *oidctest.IdP) { i.User.Claims["groups"] = []string{"pulse-users", "pulse-operators"} })
	res, err = f.flow(PurposeLogin, nil)
	must(t, err)
	if p = f.principal(res.SessionToken); !p.HasRole(users.RoleOperator) {
		t.Fatalf("mapped role missing: %v", p.User.Roles)
	}
	_, err = f.auth.Update(f.ctx, admin, p.User.ID, users.UpdateInput{Roles: ptr([]string{users.RoleViewer})})
	must(t, err)
	f.idp.Set(func(i *oidctest.IdP) { i.User.Claims["groups"] = []string{"pulse-users"} })
	res, err = f.flow(PurposeLogin, nil)
	must(t, err)
	p = f.principal(res.SessionToken)
	if p.HasRole(users.RoleOperator) || !p.HasRole(users.RoleViewer) {
		t.Fatalf("after group removal: %v (manual viewer must stay)", p.User.Roles)
	}
}

// SSO role sync never removes the last enabled administrator.
func TestSyncKeepsLastAdmin(t *testing.T) {
	f := newFx(t)
	u, err := f.auth.Create(f.ctx, admin, users.CreateInput{Email: "sso-admin@example.com", DisplayName: "SSO admin"})
	must(t, err)
	must(t, f.svc.LinkIdentity(f.ctx, admin, u.ID, f.prov.ID, "user-1"))
	_, err = f.svc.AddMapping(f.ctx, admin, f.prov.ID, "admins", users.RoleAdministrator)
	must(t, err)
	f.idp.Set(func(i *oidctest.IdP) { i.User.Claims["groups"] = []string{"admins"} })
	_, err = f.flow(PurposeLogin, nil)
	must(t, err)
	f.idp.Set(func(i *oidctest.IdP) { i.User.Claims["groups"] = []string{} })
	res, err := f.flow(PurposeLogin, nil)
	must(t, err)
	if p := f.principal(res.SessionToken); !p.HasRole(users.RoleAdministrator) {
		t.Fatal("last administrator demoted by SSO sync")
	}
}

// State is single-use, bound to the browser and expires after 10 minutes.
func TestStateSafety(t *testing.T) {
	f := newFx(t)
	u := f.user("bob@example.com")
	must(t, f.svc.LinkIdentity(f.ctx, admin, u.ID, f.prov.ID, "user-1"))
	st, err := f.svc.Start(f.ctx, StartInput{Provider: "corp", Purpose: PurposeLogin})
	must(t, err)
	q, err := f.idp.Authorize(st.RedirectURL)
	must(t, err)
	if _, err := f.svc.Callback(f.ctx, CallbackInput{State: q.Get("state"), Code: q.Get("code"), Binding: "wrong"}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("binding: %v", err)
	}
	if _, err := f.svc.Callback(f.ctx, CallbackInput{State: q.Get("state"), Code: q.Get("code"), Binding: st.Binding}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("replayed state accepted: %v", err)
	}
	st, err = f.svc.Start(f.ctx, StartInput{Provider: "corp", Purpose: PurposeLogin})
	must(t, err)
	q, err = f.idp.Authorize(st.RedirectURL)
	must(t, err)
	f.clk.Advance(11 * time.Minute)
	if _, err := f.svc.Callback(f.ctx, CallbackInput{State: q.Get("state"), Code: q.Get("code"), Binding: st.Binding}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("expired state: %v", err)
	}
	if _, err := f.svc.Callback(f.ctx, CallbackInput{State: "", Code: "x"}); !errors.Is(err, ErrInvalidState) {
		t.Fatal("empty state")
	}
	st, err = f.svc.Start(f.ctx, StartInput{Provider: "corp", Purpose: PurposeLogin})
	must(t, err)
	q, err = f.idp.Authorize(st.RedirectURL)
	must(t, err)
	if _, err := f.svc.Callback(f.ctx, CallbackInput{State: q.Get("state"), Error: "access_denied", Binding: st.Binding}); !errors.Is(err, ErrLoginRejected) {
		t.Fatalf("idp error: %v", err)
	}
}

// Signature algorithms, iss/aud/azp, time claims and nonce are enforced.
func TestIDTokenValidation(t *testing.T) {
	f := newFx(t)
	u := f.user("carol@example.com")
	must(t, f.svc.LinkIdentity(f.ctx, admin, u.ID, f.prov.ID, "user-1"))
	for _, alg := range []string{"RS256", "ES256", "EdDSA"} {
		f.idp.Set(func(i *oidctest.IdP) { i.Alg = alg })
		if _, err := f.flow(PurposeLogin, nil); err != nil {
			t.Fatalf("%s rejected: %v", alg, err)
		}
	}
	cases := map[string]func(i *oidctest.IdP){
		"alg none":  func(i *oidctest.IdP) { i.Alg = "none" },
		"alg HS256": func(i *oidctest.IdP) { i.Alg = "HS256" },
		"wrong iss": func(i *oidctest.IdP) { i.Mutate = func(c map[string]any) { c["iss"] = "https://evil.example" } },
		"wrong aud": func(i *oidctest.IdP) { i.Mutate = func(c map[string]any) { c["aud"] = "other-client" } },
		"multi aud": func(i *oidctest.IdP) { i.Mutate = func(c map[string]any) { c["aud"] = []string{"pulse", "other"} } },
		"azp mismatch": func(i *oidctest.IdP) {
			i.Mutate = func(c map[string]any) { c["aud"] = []string{"pulse", "other"}; c["azp"] = "other" }
		},
		"expired": func(i *oidctest.IdP) {
			i.Mutate = func(c map[string]any) { c["exp"] = f.clk.Now().Add(-2 * time.Minute).Unix() }
		},
		"iat in future": func(i *oidctest.IdP) {
			i.Mutate = func(c map[string]any) { c["iat"] = f.clk.Now().Add(5 * time.Minute).Unix() }
		},
		"nbf in future": func(i *oidctest.IdP) {
			i.Mutate = func(c map[string]any) { c["nbf"] = f.clk.Now().Add(3 * time.Minute).Unix() }
		},
		"wrong nonce":   func(i *oidctest.IdP) { i.Mutate = func(c map[string]any) { c["nonce"] = "attacker" } },
		"missing nonce": func(i *oidctest.IdP) { i.Mutate = func(c map[string]any) { delete(c, "nonce") } },
		"missing sub":   func(i *oidctest.IdP) { i.Mutate = func(c map[string]any) { c["sub"] = "" } },
		"missing iat":   func(i *oidctest.IdP) { i.Mutate = func(c map[string]any) { delete(c, "iat") } },
	}
	for name, set := range cases {
		t.Run(name, func(t *testing.T) {
			f.idp.Set(func(i *oidctest.IdP) { i.Alg, i.Mutate = "RS256", nil })
			f.idp.Set(set)
			if _, err := f.flow(PurposeLogin, nil); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	f.idp.Set(func(i *oidctest.IdP) {
		i.Alg, i.Mutate = "RS256", func(c map[string]any) { c["aud"] = []string{"pulse", "other"}; c["azp"] = "pulse" }
	})
	if _, err := f.flow(PurposeLogin, nil); err != nil {
		t.Fatalf("multi-aud with matching azp: %v", err)
	}
}

// The JWKS is cached, refetched on an unknown key ID and refetches are rate-limited.
func TestJWKSRotation(t *testing.T) {
	f := newFx(t)
	u := f.user("dave@example.com")
	must(t, f.svc.LinkIdentity(f.ctx, admin, u.ID, f.prov.ID, "user-1"))
	_, err := f.flow(PurposeLogin, nil)
	must(t, err)
	_, err = f.flow(PurposeLogin, nil)
	must(t, err)
	if h := f.idp.JWKSHits(); h != 1 {
		t.Fatalf("JWKS not cached: %d fetches", h)
	}
	f.idp.RotateKeys()
	if _, err := f.flow(PurposeLogin, nil); err == nil {
		t.Fatal("refetch should be rate-limited within the minimum interval")
	}
	f.clk.Advance(11 * time.Second)
	if _, err := f.flow(PurposeLogin, nil); err != nil {
		t.Fatalf("after rotation: %v", err)
	}
}

// The client secret is sealed at rest and never returned; private networks and untrusted TLS are refused.
func TestProviderSafety(t *testing.T) {
	f := newFx(t)
	b, _ := json.Marshal(f.prov)
	if strings.Contains(string(b), f.idp.ClientSecret) || !f.prov.HasClientSecret {
		t.Fatalf("secret exposed or missing: %s", b)
	}
	var raw []byte
	must(t, f.db.R().QueryRowContext(f.ctx, `SELECT client_secret_enc FROM oidc_providers WHERE id = ?`, f.prov.ID).Scan(&raw))
	if bytes.Contains(raw, []byte(f.idp.ClientSecret)) {
		t.Fatal("client secret stored in plaintext")
	}
	p, err := f.svc.UpdateProvider(f.ctx, admin, f.prov.ID, ProviderInput{Version: f.prov.Version, DisplayName: ptr("Corp")})
	must(t, err)
	if !p.HasClientSecret {
		t.Fatal("secret dropped by an unrelated update")
	}
	if _, err := f.svc.UpdateProvider(f.ctx, admin, f.prov.ID, ProviderInput{Version: 1}); !kindOf(err, users.KindConflict) {
		t.Fatalf("stale version accepted: %v", err)
	}
	for i, in := range []ProviderInput{
		{Slug: ptr("x1"), DisplayName: ptr("x"), Issuer: ptr("http://idp.example"), ClientID: ptr("c")},
		{Slug: ptr("X bad"), DisplayName: ptr("x"), Issuer: ptr("https://idp.example"), ClientID: ptr("c")},
		{Slug: ptr("x3"), DisplayName: ptr("x"), Issuer: ptr("https://idp.example"), ClientID: ptr("c"), Scopes: ptr([]string{"profile"})},
		{Slug: ptr("x4"), DisplayName: ptr("x"), Issuer: ptr("https://idp.example"), ClientID: ptr("c"), Provisioning: ptr(JIT)},
	} {
		if _, err := f.svc.CreateProvider(f.ctx, admin, in); !kindOf(err, users.KindInvalid) {
			t.Errorf("case %d accepted: %v", i, err)
		}
	}
	p, err = f.svc.UpdateProvider(f.ctx, admin, f.prov.ID, ProviderInput{Version: p.Version, AllowPrivateNetwork: ptr(false)})
	must(t, err)
	if _, err := f.svc.Start(f.ctx, StartInput{Provider: "corp", Purpose: PurposeLogin}); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("private-network IdP allowed: %v", err)
	}
	_, err = f.svc.UpdateProvider(f.ctx, admin, f.prov.ID, ProviderInput{Version: p.Version, AllowPrivateNetwork: ptr(true), CABundlePEM: ptr("")})
	must(t, err)
	if _, err := f.svc.Start(f.ctx, StartInput{Provider: "corp", Purpose: PurposeLogin}); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("untrusted TLS accepted: %v", err)
	}
}

// Step-up needs a fresh auth_time (and acr when configured); the TOTP fallback is offered when allowed.
func TestStepUp(t *testing.T) {
	f := newFx(t)
	u := f.user("frank@example.com")
	must(t, f.svc.LinkIdentity(f.ctx, admin, u.ID, f.prov.ID, "user-1"))
	res, err := f.flow(PurposeLogin, nil)
	must(t, err)
	p := f.principal(res.SessionToken)
	f.clk.Advance(10 * time.Minute)
	if err := f.auth.RequireRecentAuth(p, false); err == nil {
		t.Fatal("stale auth accepted")
	}
	res, err = f.flow(PurposeStepUp, p)
	must(t, err)
	if q := f.idp.LastAuthorizeParams(); q.Get("prompt") != "login" || q.Get("max_age") != "0" {
		t.Fatalf("step-up params %v", q)
	}
	p2 := f.principal(res.SessionToken)
	if err := f.auth.RequireRecentAuth(p2, false); err != nil || p2.Provider != "corp" {
		t.Fatalf("after step-up: %v %q", err, p2.Provider)
	}
	f.idp.Set(func(i *oidctest.IdP) {
		i.Mutate = func(c map[string]any) { c["auth_time"] = f.clk.Now().Add(-time.Hour).Unix() }
	})
	if _, err := f.flow(PurposeStepUp, p2); !errors.Is(err, ErrStepUpFailed) {
		t.Fatalf("stale auth_time: %v", err)
	}
	f.idp.Set(func(i *oidctest.IdP) { i.Mutate, i.OmitAuthTime = nil, true })
	res, err = f.flow(PurposeLogin, nil)
	must(t, err)
	p3 := f.principal(res.SessionToken)
	if _, err := f.flow(PurposeStepUp, p3); !errors.Is(err, ErrStepUpFailed) {
		t.Fatalf("missing auth_time: %v", err)
	}
	pr, err := f.svc.UpdateProvider(f.ctx, admin, f.prov.ID, ProviderInput{Version: f.prov.Version, StepUpFallback: ptr(StepUpLocalTOTP)})
	must(t, err)
	f.prov = pr
	if _, err := f.flow(PurposeStepUp, p3); !kindOf(err, users.KindMFARequired) {
		t.Fatalf("totp fallback: %v", err)
	}
}

// SSO-only needs a tested provider and an SSO-capable administrator, and stays safe afterwards (PL-USR-010).
func TestSSOOnly(t *testing.T) {
	f := newFx(t)
	root := f.user("root@example.com", users.RoleAdministrator)
	if err := f.svc.SetLocalLoginDisabled(f.ctx, admin, true); !errors.Is(err, ErrSSOOnlyPrecondition) {
		t.Fatalf("enabled without preconditions: %v", err)
	}
	must(t, f.svc.LinkIdentity(f.ctx, admin, root.ID, f.prov.ID, "user-1"))
	res, err := f.flow(PurposeLogin, nil)
	must(t, err)
	p := f.principal(res.SessionToken)
	res, err = f.flow(PurposeTest, p)
	must(t, err)
	if res.Test == nil || res.Test.LinkedUserID != root.ID || !strings.Contains(strings.Join(res.Test.EffectiveRoles, ","), users.RoleAdministrator) {
		t.Fatalf("test result %+v", res.Test)
	}
	must(t, f.svc.SetLocalLoginDisabled(f.ctx, admin, true))
	if _, err := f.auth.Login(f.ctx, "root@example.com", testPassword, f.meta); !errors.Is(err, users.ErrLocalLoginDisabled) {
		t.Fatalf("password login while SSO-only: %v", err)
	}
	if err := f.svc.UnlinkIdentity(f.ctx, admin, root.ID, f.prov.Issuer, "user-1"); err == nil {
		t.Fatal("unlinked the last SSO administrator while SSO-only")
	}
	_, err = f.svc.UpdateProvider(f.ctx, admin, f.prov.ID, ProviderInput{Version: f.prov.Version, Enabled: ptr(false)})
	if err == nil {
		t.Fatal("disabled the only provider while SSO-only")
	}
}

// Linking: a link invitation binds the next SSO login to its user; an identity is never moved.
func TestLinkInvite(t *testing.T) {
	f := newFx(t)
	u := f.user("gina@example.com", users.RoleViewer)
	tok, _, err := f.svc.IssueLinkInvite(f.ctx, admin, u.ID, f.prov.ID)
	must(t, err)
	st, err := f.svc.Start(f.ctx, StartInput{Provider: "corp", Purpose: PurposeLink, LinkToken: tok})
	must(t, err)
	q, err := f.idp.Authorize(st.RedirectURL)
	must(t, err)
	res, err := f.svc.Callback(f.ctx, CallbackInput{State: q.Get("state"), Code: q.Get("code"), Binding: st.Binding, Meta: f.meta})
	must(t, err)
	if res.UserID != u.ID {
		t.Fatalf("linked to %s", res.UserID)
	}
	if _, err := f.svc.Start(f.ctx, StartInput{Provider: "corp", Purpose: PurposeLink, LinkToken: tok}); err == nil {
		t.Fatal("link invitation reused")
	}
	other := f.user("hank@example.com")
	if err := f.svc.LinkIdentity(f.ctx, admin, other.ID, f.prov.ID, "user-1"); !kindOf(err, users.KindConflict) {
		t.Fatalf("identity moved: %v", err)
	}
	res, err = f.flow(PurposeLogin, nil)
	must(t, err)
	if res.UserID != u.ID {
		t.Fatal("login went to the wrong user")
	}
}

func TestSanitizeReturnTo(t *testing.T) {
	for in, want := range map[string]string{
		"/explorer?x=1": "/explorer?x=1", "": "/", "https://evil.example/": "/", "//evil.example": "/", "/api/admin/v1/users": "/",
		"/auth/oidc/callback": "/", "/a/../api/x": "/", "/%2e%2e/api": "/", "/#/../api/x": "/", "/ok\r\nX: y": "/",
	} {
		if got := SanitizeReturnTo(in); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}
