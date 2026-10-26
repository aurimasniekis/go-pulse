package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/aurimasniekis/go-pulse/internal/rbac"
	"github.com/aurimasniekis/go-pulse/internal/users"
)

// Listener kinds the dashboard is served on (ADR-0038). Sessions are bound to the listener that created them.
const (
	ListenerLoopback = "loopback" // the admin listener on host loopback, reached through an SSH tunnel
	ListenerPublic   = "public"   // the dashboard hostname on the Cloudflare-fronted public listener
)

// listener describes where an admin request arrived.
type listener struct {
	kind   string
	secure bool // https: __Host- cookies with Secure, HSTS
}

func (l listener) sessionCookie() string {
	if l.secure {
		return "__Host-pulse_session"
	}
	return "pulse_session"
}

func (l listener) oidcCookie() string {
	if l.secure {
		return "__Host-pulse_oidc"
	}
	return "pulse_oidc"
}

func (l listener) scheme() string {
	if l.secure {
		return "https"
	}
	return "http"
}

type listenerKey struct{}
type principalKey struct{}
type sessionTokenKey struct{}

func listenerFrom(ctx context.Context) listener {
	l, _ := ctx.Value(listenerKey{}).(listener)
	return l
}

// principalFrom returns the authenticated user of the request, or nil.
func principalFrom(ctx context.Context) *users.Principal {
	p, _ := ctx.Value(principalKey{}).(*users.Principal)
	return p
}

func sessionTokenFrom(ctx context.Context) string {
	t, _ := ctx.Value(sessionTokenKey{}).(string)
	return t
}

func (d *Deps) meta(ctx context.Context, r *http.Request) users.SessionMeta {
	ip, _ := ctx.Value(clientIPKey{}).(string)
	ua := ""
	if r != nil {
		ua = r.UserAgent()
	}
	return users.SessionMeta{IP: ip, UserAgent: ua, Listener: listenerFrom(ctx).kind}
}

const bearerPrefix = "Bearer "

// authenticate resolves the session cookie or a personal access token (on the loopback listener, or on the public one
// when AdminPublicPATs allows it) into a principal, and refuses cross-site state-changing requests: a mutation carrying a session cookie, or any
// unauthenticated mutation (sign-in endpoints), must come from the dashboard's own origin.
func (d *Deps) authenticate(l listener, next http.Handler) http.Handler {
	authLimit := newIPLimiter(20.0/60, 20) // sign-in attempts per address: 20 per minute
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), listenerKey{}, l)
		ip, _ := ctx.Value(clientIPKey{}).(string)
		var p *users.Principal
		bearer := strings.HasPrefix(r.Header.Get("Authorization"), bearerPrefix+users.PATPrefix)
		switch {
		case bearer && (l.kind == ListenerLoopback || d.AdminPublicPATs) && d.Users != nil:
			var err error
			if p, err = d.Users.AuthenticatePAT(ctx, strings.TrimPrefix(r.Header.Get("Authorization"), bearerPrefix), ip, l.kind); err != nil {
				if l.kind == ListenerPublic && errors.Is(err, users.ErrUnauthorized) && d.OnAuthFailure != nil {
					d.OnAuthFailure(ip, "admin_token")
				}
				writeProblem(w, err)
				return
			}
		case bearer:
			writeProblem(w, users.Errf(users.KindUnauthorized, "personal access tokens work only on the loopback admin listener"))
			return
		default:
			if ck, err := r.Cookie(l.sessionCookie()); err == nil && ck.Value != "" && d.Users != nil {
				ctx = context.WithValue(ctx, sessionTokenKey{}, ck.Value)
				if pp, err := d.Users.Authenticate(ctx, ck.Value, l.kind); err == nil {
					p = pp
				}
			}
		}
		if mutating(r.Method) && !bearer {
			if !sameOrigin(r, l) {
				writeProblem(w, users.Errf(users.KindForbidden, "cross-site request refused"))
				return
			}
			if p == nil && (strings.HasPrefix(r.URL.Path, AdminPrefix+"/auth/") || strings.HasPrefix(r.URL.Path, "/auth/")) && !authLimit.allow(ip) {
				w.Header().Set("Retry-After", "60")
				writeProblem(w, users.ErrThrottled)
				return
			}
		}
		if p != nil {
			ctx = context.WithValue(ctx, principalKey{}, p)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func mutating(m string) bool {
	return m != http.MethodGet && m != http.MethodHead && m != http.MethodOptions
}

// sameOrigin requires Origin to equal the request's own origin (the Host was checked by hostCheck), or
// Sec-Fetch-Site: same-origin when a browser omits Origin.
func sameOrigin(r *http.Request, l listener) bool {
	if o := r.Header.Get("Origin"); o != "" {
		return strings.EqualFold(o, l.scheme()+"://"+r.Host)
	}
	return r.Header.Get("Sec-Fetch-Site") == "same-origin"
}

// authorize is the per-operation check on the admin API: the operation's permission (opRules), the session's CSRF
// token on mutations, and recent authentication where the rule asks for it.
func (d *Deps) authorizeOps(api huma.API) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		op := ctx.Operation()
		rl, ok := opRules[op.OperationID]
		if !ok {
			// Fail closed: an operation without a rule is unreachable (and TestEveryOperationHasARule fails).
			writeHumaProblem(api, ctx, rbac.ErrForbidden)
			return
		}
		p := principalFrom(ctx.Context())
		if err := rbac.Authorize(p, rl.perm); err != nil {
			writeHumaProblem(api, ctx, err)
			return
		}
		if p != nil && rl.perm != rbac.Public && mutating(op.Method) && !p.VerifyCSRF(ctx.Header(CSRFHeader)) {
			writeHumaProblem(api, ctx, users.Errf(users.KindForbidden, "missing or invalid CSRF token"))
			return
		}
		if rl.reauth || rl.mfa {
			if err := d.Users.RequireRecentAuth(p, rl.mfa); err != nil {
				writeHumaProblem(api, ctx, err)
				return
			}
		}
		next(ctx)
	}
}

// Problem is an RFC 9457 problem with a stable machine-readable code (reauth_required, mfa_required, …) that the
// dashboard uses to drive sign-in and re-authentication.
type Problem struct {
	Status int    `json:"status"`
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
	Code   string `json:"code,omitempty"`
}

func (p *Problem) Error() string  { return p.Detail }
func (p *Problem) GetStatus() int { return p.Status }
func (p *Problem) ContentType(string) string {
	return "application/problem+json"
}

var kindStatus = map[users.Kind]int{
	users.KindInvalid: http.StatusUnprocessableEntity, users.KindUnauthorized: http.StatusUnauthorized, users.KindForbidden: http.StatusForbidden,
	users.KindNotFound: http.StatusNotFound, users.KindConflict: http.StatusConflict, users.KindPrecondition: http.StatusPreconditionFailed,
	users.KindRateLimited: http.StatusTooManyRequests, users.KindReauth: http.StatusUnauthorized, users.KindMFARequired: http.StatusUnauthorized,
	users.KindUnavailable: http.StatusServiceUnavailable,
}

// toProblem converts a users/oidc error; other errors are not user-facing (nil).
func toProblem(err error) *Problem {
	var ue *users.Error
	if !errors.As(err, &ue) {
		return nil
	}
	st := kindStatus[ue.Kind]
	if st == 0 {
		st = http.StatusBadRequest
	}
	return &Problem{Status: st, Title: http.StatusText(st), Detail: ue.Msg, Code: string(ue.Kind)}
}

// authErr maps a users/oidc error for a handler return; anything else is an opaque 500.
func (d *Deps) authErr(op string, err error) error {
	if p := toProblem(err); p != nil {
		return p
	}
	return d.internalErr(op, err)
}

func writeProblem(w http.ResponseWriter, err error) {
	p := toProblem(err)
	if p == nil {
		p = &Problem{Status: http.StatusInternalServerError, Title: "Internal Server Error"}
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}

func writeHumaProblem(_ huma.API, ctx huma.Context, err error) {
	p := toProblem(err)
	if p == nil {
		p = &Problem{Status: http.StatusInternalServerError, Title: "Internal Server Error"}
	}
	ctx.SetHeader("Content-Type", "application/problem+json")
	ctx.SetHeader("Cache-Control", "no-store")
	ctx.SetStatus(p.Status)
	_ = json.NewEncoder(ctx.BodyWriter()).Encode(p)
}

// sessionCookie returns the Set-Cookie for a session token ("" clears it).
func sessionCookie(l listener, token string) http.Cookie {
	c := http.Cookie{Name: l.sessionCookie(), Value: token, Path: "/", HttpOnly: true, Secure: l.secure, SameSite: http.SameSiteLaxMode} //nolint:gosec // Secure is off only on the plain-http loopback listener
	if token == "" {
		c.MaxAge = -1
	}
	return c
}
