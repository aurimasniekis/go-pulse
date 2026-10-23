// Package auth is the admin Authenticator seam (PL-ADM-002, ADR-0008). Mode "none" is the only implementation;
// "oidc" (central SSO, R-02) must satisfy docs/operator/sso.md before it may be enabled.
package auth

import (
	"context"
	"errors"
	"net/http"
)

// Principal is the authenticated admin.
type Principal struct {
	Subject string // stable identifier, e.g. "admin" for mode none, the OIDC subject later
	Name    string
}

// Authenticator authenticates admin requests.
type Authenticator interface {
	// Authenticate returns the principal or ErrUnauthenticated. Implementations may write a redirect (OIDC login) only
	// through LoginURL; they never write to the response here.
	Authenticate(r *http.Request) (*Principal, error)
	Mode() string
}

// ErrUnauthenticated means the request carries no valid session.
var ErrUnauthenticated = errors.New("unauthenticated")

// None trusts every request. It is safe only because the admin listener is reachable solely via host loopback
// (enforced by config validation in the prod profile, PL-ADM-003).
type None struct{}

// Authenticate returns the fixed local admin principal.
func (None) Authenticate(*http.Request) (*Principal, error) {
	return &Principal{Subject: "admin", Name: "admin (auth none)"}, nil
}

// Mode returns "none".
func (None) Mode() string { return "none" }

// New returns the authenticator for a mode.
func New(mode string) (Authenticator, error) {
	switch mode {
	case "none", "":
		return None{}, nil
	case "oidc":
		return nil, errors.New("auth mode oidc is not implemented (DECISIONS.md R-02, docs/operator/sso.md)")
	default:
		return nil, errors.New("unknown auth mode " + mode)
	}
}

type ctxKey struct{}

// WithPrincipal stores the principal in a context.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// From returns the principal from a context, or nil.
func From(ctx context.Context) *Principal {
	p, _ := ctx.Value(ctxKey{}).(*Principal)
	return p
}

// Middleware authenticates every request with a and stores the principal; failures get 401.
func Middleware(a Authenticator, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, err := a.Authenticate(r)
		if err != nil {
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
	})
}
