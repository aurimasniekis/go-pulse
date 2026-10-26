package users

import (
	"bytes"
	"context"
	"encoding/base32"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aurimasniekis/go-pulse/internal/audit"
	"github.com/aurimasniekis/go-pulse/internal/store"
)

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
	t   *testing.T
	ctx context.Context
	clk *clock
	db  *store.DB
	svc *Service
}

var root = audit.Actor{Name: "user:root@example.com"}

const pw = "correct horse battery staple"

var meta = SessionMeta{IP: "198.51.100.9", Listener: "loopback"}

func newFx(t *testing.T) *fx {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{Path: filepath.Join(t.TempDir(), "pulse.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	keys, err := DeriveKeys(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	clk := &clock{now: time.Now().UTC().Truncate(time.Millisecond)}
	return &fx{t: t, ctx: ctx, clk: clk, db: db, svc: New(db, keys, Options{Now: clk.Now})}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// bootstrap creates the first administrator (MFA required) through the setup token.
func (f *fx) bootstrap() *User {
	f.t.Helper()
	tok, _, err := f.svc.IssueSetupToken(f.ctx)
	must(f.t, err)
	u, err := f.svc.Bootstrap(f.ctx, BootstrapInput{Token: tok, Email: "root@example.com", Password: pw}, "")
	must(f.t, err)
	if _, _, err := f.svc.IssueSetupToken(f.ctx); err == nil {
		f.t.Fatal("a second setup token was issued")
	}
	return u
}

// withPassword creates a user and sets a password through a reset link.
func (f *fx) withPassword(email string, roles ...string) *User {
	f.t.Helper()
	u, err := f.svc.Create(f.ctx, root, CreateInput{Email: email, DisplayName: email, Roles: roles})
	must(f.t, err)
	tok, _, err := f.svc.IssueResetToken(f.ctx, root, u.ID)
	must(f.t, err)
	must(f.t, f.svc.ResetPassword(f.ctx, tok, pw, ""))
	return u
}

func (f *fx) code(secret []byte) string { return TOTPCode(secret, TOTPStep(f.clk.Now())) }

// enroll signs in (limited), enrolls TOTP and completes sign-in with a later code; returns the token and secret.
func (f *fx) enroll(email string) (string, []byte, []string) {
	f.t.Helper()
	res, err := f.svc.Login(f.ctx, email, pw, meta)
	must(f.t, err)
	if !res.MustEnroll || !res.Principal.Limited {
		f.t.Fatalf("expected enrollment, got %+v", res)
	}
	e, err := f.svc.BeginTOTP(f.ctx, res.Principal)
	must(f.t, err)
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(e.Secret)
	must(f.t, err)
	codes, tok, err := f.svc.ConfirmTOTP(f.ctx, res.Principal, f.code(secret), meta)
	must(f.t, err)
	if tok != "" || len(codes) != RecoveryCodeCount {
		f.t.Fatalf("limited enrollment rotated the session or lacks codes: %q %d", tok, len(codes))
	}
	p, err := f.svc.Authenticate(f.ctx, res.Token, meta.Listener)
	must(f.t, err)
	if !p.Limited {
		f.t.Fatal("enrollment completed sign-in without a second code")
	}
	if _, err := f.svc.VerifyMFA(f.ctx, p, f.code(secret), meta); err == nil {
		f.t.Fatal("the enrollment code was accepted again")
	}
	f.clk.Advance(30 * time.Second)
	full, err := f.svc.VerifyMFA(f.ctx, p, f.code(secret), meta)
	must(f.t, err)
	return full, secret, codes
}

// Bootstrap once; the administrator must enroll TOTP before the session can be used (PL-USR-001, PL-USR-006).
func TestBootstrapAndMandatoryMFA(t *testing.T) {
	f := newFx(t)
	f.bootstrap()
	tok, secret, codes := f.enroll("root@example.com")
	p, err := f.svc.Authenticate(f.ctx, tok, meta.Listener)
	must(t, err)
	if p.Limited || p.MFAAt == nil || !p.HasRole(RoleAdministrator) {
		t.Fatalf("%+v", p)
	}
	// A recovery code works once.
	f.clk.Advance(time.Minute)
	res, err := f.svc.Login(f.ctx, "root@example.com", pw, meta)
	must(t, err)
	if !res.MFAPending {
		t.Fatal("second login skipped MFA")
	}
	if _, err := f.svc.VerifyMFA(f.ctx, res.Principal, codes[0], meta); err != nil {
		t.Fatalf("recovery code: %v", err)
	}
	res, err = f.svc.Login(f.ctx, "root@example.com", pw, meta)
	must(t, err)
	if _, err := f.svc.VerifyMFA(f.ctx, res.Principal, codes[0], meta); err == nil {
		t.Fatal("recovery code reused")
	}
	_ = secret
}

// Every login failure looks the same and repeated failures are throttled (PL-USR-005).
func TestLoginFailuresAndThrottle(t *testing.T) {
	f := newFx(t)
	f.withPassword("ann@example.com", RoleViewer)
	_, e1 := f.svc.Login(f.ctx, "ann@example.com", "wrong password!", meta)
	_, e2 := f.svc.Login(f.ctx, "nobody@example.com", pw, meta)
	if !errors.Is(e1, ErrInvalidCredentials) || !errors.Is(e2, ErrInvalidCredentials) || e1.Error() != e2.Error() {
		t.Fatalf("%v / %v", e1, e2)
	}
	var err error
	for range 6 {
		_, err = f.svc.Login(f.ctx, "ann@example.com", "wrong password!", SessionMeta{IP: "203.0.113.5", Listener: "loopback"})
	}
	if !errors.Is(err, ErrThrottled) {
		t.Fatalf("not throttled: %v", err)
	}
	if _, err := f.svc.Login(f.ctx, "ann@example.com", pw, SessionMeta{IP: "203.0.113.6", Listener: "loopback"}); !errors.Is(err, ErrThrottled) {
		t.Fatalf("account throttle bypassed from another address: %v", err)
	}
	f.clk.Advance(throttleMax + time.Second)
	if _, err := f.svc.Login(f.ctx, "ann@example.com", pw, meta); err != nil {
		t.Fatalf("after the delay: %v", err)
	}
	// Disabled users cannot sign in, with the same error.
	u, err := f.svc.GetByEmail(f.ctx, "ann@example.com")
	must(t, err)
	off := false
	_, err = f.svc.Update(f.ctx, root, u.ID, UpdateInput{Enabled: &off})
	must(t, err)
	if _, err := f.svc.Login(f.ctx, "ann@example.com", pw, meta); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("disabled user: %v", err)
	}
}

// Sessions: idle and absolute expiry, listener binding, CSRF, recent authentication, rotation on reauth.
func TestSessions(t *testing.T) {
	f := newFx(t)
	f.withPassword("ben@example.com", RoleViewer)
	res, err := f.svc.Login(f.ctx, "ben@example.com", pw, meta)
	must(t, err)
	p := res.Principal
	if p.Limited || !p.VerifyCSRF(p.CSRFToken) || p.VerifyCSRF("x") || p.VerifyCSRF("") {
		t.Fatalf("%+v", p)
	}
	if _, err := f.svc.Authenticate(f.ctx, res.Token, "public"); err == nil {
		t.Fatal("loopback session accepted on the public listener")
	}
	must(t, f.svc.RequireRecentAuth(p, false))
	f.clk.Advance(6 * time.Minute)
	p, err = f.svc.Authenticate(f.ctx, res.Token, meta.Listener)
	must(t, err)
	if err := f.svc.RequireRecentAuth(p, false); !errors.Is(err, ErrReauthRequired) {
		t.Fatalf("stale: %v", err)
	}
	tok, err := f.svc.Reauth(f.ctx, p, pw, "", meta)
	must(t, err)
	if _, err := f.svc.Authenticate(f.ctx, res.Token, meta.Listener); err == nil {
		t.Fatal("old token survived rotation")
	}
	p, err = f.svc.Authenticate(f.ctx, tok, meta.Listener)
	must(t, err)
	must(t, f.svc.RequireRecentAuth(p, false))
	f.clk.Advance(31 * time.Minute)
	if _, err := f.svc.Authenticate(f.ctx, tok, meta.Listener); err == nil {
		t.Fatal("idle session accepted")
	}
	res, err = f.svc.Login(f.ctx, "ben@example.com", pw, meta)
	must(t, err)
	for range 25 {
		f.clk.Advance(29 * time.Minute)
		if _, err := f.svc.Authenticate(f.ctx, res.Token, meta.Listener); err != nil {
			break
		}
	}
	if _, err := f.svc.Authenticate(f.ctx, res.Token, meta.Listener); err == nil {
		t.Fatal("session outlived its absolute lifetime")
	}
}

// Role changes and disabling revoke sessions; the last administrator cannot be removed (PL-USR-003).
func TestRolesAndLastAdmin(t *testing.T) {
	f := newFx(t)
	admin := f.bootstrap()
	viewer := f.withPassword("cid@example.com", RoleViewer)
	res, err := f.svc.Login(f.ctx, "cid@example.com", pw, meta)
	must(t, err)
	_, err = f.svc.Update(f.ctx, root, viewer.ID, UpdateInput{Roles: &[]string{RoleOperator}})
	must(t, err)
	if _, err := f.svc.Authenticate(f.ctx, res.Token, meta.Listener); err == nil {
		t.Fatal("session survived a role change")
	}
	if _, err := f.svc.Update(f.ctx, root, admin.ID, UpdateInput{Roles: &[]string{RoleViewer}}); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("demoted the last administrator: %v", err)
	}
	off := false
	if _, err := f.svc.Update(f.ctx, root, admin.ID, UpdateInput{Enabled: &off}); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("disabled the last administrator: %v", err)
	}
	if err := f.svc.Delete(f.ctx, root, admin.ID); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("deleted the last administrator: %v", err)
	}
	if _, err := f.svc.Update(f.ctx, root, viewer.ID, UpdateInput{Roles: &[]string{"root"}}); KindOf(err) != KindInvalid {
		t.Fatalf("unknown role: %v", err)
	}
}

// Invitations and reset links are single use; a reset revokes every session.
func TestInvitationsAndResets(t *testing.T) {
	f := newFx(t)
	tok, _, err := f.svc.Invite(f.ctx, root, "dee@example.com", []string{RoleAuditor})
	must(t, err)
	inv, err := f.svc.ListInvitations(f.ctx)
	must(t, err)
	if len(inv) != 1 || inv[0].Email != "dee@example.com" || strings.Contains(inv[0].ID, tok) {
		t.Fatalf("%+v", inv)
	}
	if _, err := f.svc.AcceptInvitation(f.ctx, tok, "Dee", "short", ""); KindOf(err) != KindInvalid {
		t.Fatalf("short password: %v", err)
	}
	u, err := f.svc.AcceptInvitation(f.ctx, tok, "Dee", pw, "")
	must(t, err)
	if u.ProvisionedVia != "invite" || len(u.Roles) != 1 || u.Roles[0] != RoleAuditor {
		t.Fatalf("%+v", u)
	}
	if _, err := f.svc.AcceptInvitation(f.ctx, tok, "Dee", pw, ""); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("invitation reused: %v", err)
	}
	res, err := f.svc.Login(f.ctx, "dee@example.com", pw, meta)
	must(t, err)
	reset, _, err := f.svc.IssueResetToken(f.ctx, root, u.ID)
	must(t, err)
	f.clk.Advance(2 * time.Hour)
	if err := f.svc.ResetPassword(f.ctx, reset, "another long password", ""); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("expired reset link: %v", err)
	}
	reset, _, err = f.svc.IssueResetToken(f.ctx, root, u.ID)
	must(t, err)
	must(t, f.svc.ResetPassword(f.ctx, reset, "another long password", ""))
	if _, err := f.svc.Authenticate(f.ctx, res.Token, meta.Listener); err == nil {
		t.Fatal("session survived a password reset")
	}
}

// Break-glass recovery removes the lost factor, revokes sessions and issues a password reset (PL-USR-007).
func TestBreakGlass(t *testing.T) {
	f := newFx(t)
	admin := f.bootstrap()
	tok, _, _ := f.enroll("root@example.com")
	rec, _, err := f.svc.IssueRecoveryLogin(f.ctx, admin.ID, "")
	must(t, err)
	res, err := f.svc.RecoveryLogin(f.ctx, rec, meta)
	must(t, err)
	if !res.MFAReset || res.ResetToken == "" || !res.Principal.NeedsEnroll {
		t.Fatalf("%+v", res)
	}
	if _, err := f.svc.Authenticate(f.ctx, tok, meta.Listener); err == nil {
		t.Fatal("old session survived break-glass")
	}
	if _, err := f.svc.RecoveryLogin(f.ctx, rec, meta); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("recovery token reused: %v", err)
	}
	if _, _, err := f.svc.IssueRecoveryLogin(f.ctx, "", "new-admin@example.com"); err != nil {
		t.Fatalf("new administrator: %v", err)
	}
}

// Personal access tokens carry one of the user's manual roles, are capped at every request, and expire.
func TestPersonalAccessTokens(t *testing.T) {
	f := newFx(t)
	f.bootstrap()
	op := f.withPassword("eve@example.com", RoleOperator)
	res, err := f.svc.Login(f.ctx, "eve@example.com", pw, meta)
	must(t, err)
	if !res.MustEnroll {
		t.Fatal("operators must enroll TOTP")
	}
	tok, _, _ := f.enroll("eve@example.com")
	p, err := f.svc.Authenticate(f.ctx, tok, meta.Listener)
	must(t, err)
	if _, _, err := f.svc.CreatePAT(f.ctx, p, "cli", RoleAdministrator, 0, nil, ""); KindOf(err) != KindInvalid {
		t.Fatalf("token with a role the user lacks: %v", err)
	}
	pat, secret, err := f.svc.CreatePAT(f.ctx, p, "cli", RoleOperator, 24*time.Hour, nil, "")
	must(t, err)
	tp, err := f.svc.AuthenticatePAT(f.ctx, secret, "127.0.0.1", "loopback")
	must(t, err)
	if tp.Method != MethodToken || !tp.HasRole(RoleOperator) || !tp.VerifyCSRF("") || f.svc.RequireRecentAuth(tp, true) != nil {
		t.Fatalf("%+v", tp)
	}
	if _, _, err := f.svc.CreatePAT(f.ctx, tp, "nested", RoleOperator, 0, nil, ""); err == nil {
		t.Fatal("a token created a token")
	}
	_, err = f.svc.Update(f.ctx, root, op.ID, UpdateInput{Roles: &[]string{RoleViewer}})
	must(t, err)
	tp, err = f.svc.AuthenticatePAT(f.ctx, secret, "127.0.0.1", "loopback")
	must(t, err)
	if len(tp.User.Roles) != 0 {
		t.Fatalf("token kept a role the user lost: %v", tp.User.Roles)
	}
	must(t, f.svc.RevokePAT(f.ctx, root, "", pat.ID))
	if _, err := f.svc.AuthenticatePAT(f.ctx, secret, "127.0.0.1", "loopback"); err == nil {
		t.Fatal("revoked token accepted")
	}
	if _, err := f.svc.AuthenticatePAT(f.ctx, "pulse_pat_guess", "127.0.0.1", "loopback"); err == nil {
		t.Fatal("guessed token accepted")
	}
}

// TestPATNetworks: a token is accepted on the public dashboard host only from the networks it was created with
// (ADR-0041); without networks it stays loopback-only.
func TestPATNetworks(t *testing.T) {
	f := newFx(t)
	f.bootstrap()
	for _, bad := range [][]string{{"0.0.0.0/0"}, {"198.51.0.0/15"}, {"2001:db8::/32"}, {"not-an-ip"}} {
		if _, _, err := f.svc.IssuePATOnHost(f.ctx, "terraform@example.com", "terraform", RoleOperator, 0, bad, true); KindOf(err) != KindInvalid {
			t.Fatalf("%v: %v", bad, err)
		}
	}
	if _, err := f.svc.GetByEmail(f.ctx, "terraform@example.com"); err == nil {
		t.Fatal("a refused token still created its user")
	}
	pat, secret, err := f.svc.IssuePATOnHost(f.ctx, "terraform@example.com", "terraform", RoleOperator, 0,
		[]string{" 203.0.113.7 ", "2001:db8:1::/48", "203.0.113.7/32"}, true)
	must(t, err)
	if want := []string{"203.0.113.7/32", "2001:db8:1::/48"}; !slices.Equal(pat.CIDRs, want) {
		t.Fatalf("cidrs %v, want %v", pat.CIDRs, want)
	}
	if ts, _ := f.svc.ListPATs(f.ctx, pat.UserID); len(ts) != 1 || !slices.Equal(ts[0].CIDRs, pat.CIDRs) {
		t.Fatalf("listed %+v", ts)
	}
	for ip, ok := range map[string]bool{"203.0.113.7": true, "::ffff:203.0.113.7": true, "2001:db8:1:2::9": true, "203.0.113.8": false, "": false} {
		p, err := f.svc.AuthenticatePAT(f.ctx, secret, ip, "public")
		if ok != (err == nil) || ok && p.Listener != "public" {
			t.Errorf("public from %q: %+v %v", ip, p, err)
		}
	}
	if _, err := f.svc.AuthenticatePAT(f.ctx, secret, "127.0.0.1", "loopback"); err != nil {
		t.Fatalf("loopback: %v", err)
	}
	_, local, err := f.svc.IssuePATOnHost(f.ctx, "terraform@example.com", "local", RoleOperator, 0, nil, false)
	must(t, err)
	if _, err := f.svc.AuthenticatePAT(f.ctx, local, "203.0.113.7", "public"); err == nil {
		t.Fatal("a token without networks was accepted on the public host")
	}
}

func TestTOTPVectors(t *testing.T) {
	// RFC 6238 appendix B (SHA-1), truncated to six digits.
	secret := []byte("12345678901234567890")
	for ts, want := range map[int64]string{59: "287082", 1111111109: "081804", 1234567890: "005924", 2000000000: "279037"} {
		if got := TOTPCode(secret, ts/30); got != want {
			t.Errorf("T=%d: %s, want %s", ts, got, want)
		}
	}
	if _, ok := VerifyTOTP(secret, TOTPCode(secret, TOTPStep(time.Unix(1000, 0))), time.Unix(1000, 0), TOTPStep(time.Unix(1000, 0))); ok {
		t.Fatal("step reuse accepted")
	}
}

func TestPasswordHashing(t *testing.T) {
	h, err := HashPassword([]byte(pw), CurrentParams)
	must(t, err)
	if ok, err := VerifyPassword([]byte(pw), h); !ok || err != nil {
		t.Fatal("verify")
	}
	if ok, _ := VerifyPassword([]byte(pw+"x"), h); ok {
		t.Fatal("wrong password verified")
	}
	if _, err := NormalizePassword(strings.Repeat("a", MaxPasswordBytes+1), 12); err == nil {
		t.Fatal("over-long password accepted (must never be truncated)")
	}
}
