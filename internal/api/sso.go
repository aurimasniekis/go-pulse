package api

import (
	"context"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/aurimasniekis/go-pulse/internal/oidc"
	"github.com/aurimasniekis/go-pulse/internal/rbac"
	"github.com/aurimasniekis/go-pulse/internal/users"
)

// SSOProvider is a provider with the redirect URI to register at the identity provider.
type SSOProvider struct {
	*oidc.Provider
	RedirectURI string `json:"redirect_uri"`
}

// ssoTests keeps the latest test-login preview per user and provider for 10 minutes (it is fetched by the settings
// page after the redirect; nothing sensitive goes into the URL).
type ssoTests struct {
	mu sync.Mutex
	m  map[string]ssoTest
}

type ssoTest struct {
	res *oidc.TestResult
	at  time.Time
}

func (s *ssoTests) put(key string, r *oidc.TestResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]ssoTest{}
	}
	for k, v := range s.m {
		if time.Since(v.at) > 10*time.Minute {
			delete(s.m, k)
		}
	}
	s.m[key] = ssoTest{res: r, at: time.Now()}
}

func (s *ssoTests) get(key string) *oidc.TestResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.m[key]; ok && time.Since(t.at) <= 10*time.Minute {
		return t.res
	}
	return nil
}

func registerSSO(api huma.API, d *Deps) {
	tags := []string{"sso"}
	wrap := func(p *oidc.Provider) SSOProvider { return SSOProvider{Provider: p, RedirectURI: d.OIDC.RedirectURI()} }
	type idPath struct {
		ID string `path:"id" maxLength:"64"`
	}
	huma.Register(api, huma.Operation{OperationID: "list-sso-providers", Method: http.MethodGet, Path: "/sso/providers", Summary: "SSO providers", Tags: tags},
		func(ctx context.Context, _ *struct{}) (*struct{ Body []SSOProvider }, error) {
			ps, err := d.OIDC.ListProviders(ctx)
			if err != nil {
				return nil, d.internalErr("list providers", err)
			}
			out := make([]SSOProvider, len(ps))
			for i, p := range ps {
				out[i] = wrap(p)
			}
			return &struct{ Body []SSOProvider }{out}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "get-sso-provider", Method: http.MethodGet, Path: "/sso/providers/{id}", Summary: "An SSO provider", Tags: tags},
		func(ctx context.Context, in *idPath) (*struct{ Body SSOProvider }, error) {
			p, err := d.OIDC.GetProvider(ctx, in.ID)
			if err != nil {
				return nil, d.authErr("get provider", err)
			}
			return &struct{ Body SSOProvider }{wrap(p)}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "create-sso-provider", Method: http.MethodPost, Path: "/sso/providers", Summary: "Add an SSO provider",
		DefaultStatus: http.StatusCreated, Tags: tags}, func(ctx context.Context, in *struct{ Body oidc.ProviderInput }) (*struct{ Body SSOProvider }, error) {
		pr, ip := d.actorP(ctx)
		p, err := d.OIDC.CreateProvider(ctx, users.Actor(pr.User, ip), in.Body)
		if err != nil {
			return nil, d.authErr("create provider", err)
		}
		return &struct{ Body SSOProvider }{wrap(p)}, nil
	})
	huma.Register(api, huma.Operation{OperationID: "update-sso-provider", Method: http.MethodPut, Path: "/sso/providers/{id}", Summary: "Change an SSO provider",
		Tags: tags}, func(ctx context.Context, in *struct {
		ID   string `path:"id" maxLength:"64"`
		Body oidc.ProviderInput
	}) (*struct{ Body SSOProvider }, error) {
		pr, ip := d.actorP(ctx)
		p, err := d.OIDC.UpdateProvider(ctx, users.Actor(pr.User, ip), in.ID, in.Body)
		if err != nil {
			return nil, d.authErr("update provider", err)
		}
		return &struct{ Body SSOProvider }{wrap(p)}, nil
	})
	huma.Register(api, huma.Operation{OperationID: "delete-sso-provider", Method: http.MethodDelete, Path: "/sso/providers/{id}", Summary: "Remove an SSO provider",
		DefaultStatus: http.StatusNoContent, Tags: tags}, func(ctx context.Context, in *idPath) (*struct{}, error) {
		pr, ip := d.actorP(ctx)
		if err := d.OIDC.DeleteProvider(ctx, users.Actor(pr.User, ip), in.ID); err != nil {
			return nil, d.authErr("delete provider", err)
		}
		return nil, nil
	})
	huma.Register(api, huma.Operation{OperationID: "list-sso-mappings", Method: http.MethodGet, Path: "/sso/providers/{id}/mappings",
		Summary: "Group → role mappings", Tags: tags}, func(ctx context.Context, in *idPath) (*struct{ Body []oidc.Mapping }, error) {
		v, err := d.OIDC.ListMappings(ctx, in.ID)
		if err != nil {
			return nil, d.internalErr("mappings", err)
		}
		return &struct{ Body []oidc.Mapping }{v}, nil
	})
	huma.Register(api, huma.Operation{OperationID: "add-sso-mapping", Method: http.MethodPost, Path: "/sso/providers/{id}/mappings", Summary: "Map a group to a role",
		DefaultStatus: http.StatusCreated, Tags: tags}, func(ctx context.Context, in *struct {
		ID   string `path:"id" maxLength:"64"`
		Body struct {
			GroupValue string `json:"group_value" maxLength:"256"`
			Role       string `json:"role" enum:"administrator,operator,auditor,viewer"`
		}
	}) (*struct{ Body *oidc.Mapping }, error) {
		pr, ip := d.actorP(ctx)
		m, err := d.OIDC.AddMapping(ctx, users.Actor(pr.User, ip), in.ID, in.Body.GroupValue, in.Body.Role)
		if err != nil {
			return nil, d.authErr("add mapping", err)
		}
		return &struct{ Body *oidc.Mapping }{m}, nil
	})
	huma.Register(api, huma.Operation{OperationID: "delete-sso-mapping", Method: http.MethodDelete, Path: "/sso/providers/{id}/mappings/{mapping}",
		Summary: "Remove a mapping", DefaultStatus: http.StatusNoContent, Tags: tags}, func(ctx context.Context, in *struct {
		ID      string `path:"id" maxLength:"64"`
		Mapping string `path:"mapping" maxLength:"64"`
	}) (*struct{}, error) {
		pr, ip := d.actorP(ctx)
		if err := d.OIDC.DeleteMapping(ctx, users.Actor(pr.User, ip), in.ID, in.Mapping); err != nil {
			return nil, d.authErr("delete mapping", err)
		}
		return nil, nil
	})
	huma.Register(api, huma.Operation{OperationID: "get-sso-test-result", Method: http.MethodGet, Path: "/sso/providers/{id}/test-result",
		Summary: "The result of my latest test login with this provider", Tags: tags}, func(ctx context.Context, in *idPath) (*struct{ Body *oidc.TestResult }, error) {
		r := d.ssoTests.get(principalFrom(ctx).User.ID + "|" + in.ID)
		if r == nil {
			return nil, d.authErr("test result", users.Errf(users.KindNotFound, "no recent test login for this provider; run one first"))
		}
		return &struct{ Body *oidc.TestResult }{r}, nil
	})
	type authSettings struct {
		LocalLoginDisabled bool `json:"local_login_disabled" doc:"SSO-only: password sign-in is refused (break-glass recovery still works)"`
	}
	huma.Register(api, huma.Operation{OperationID: "get-auth-settings", Method: http.MethodGet, Path: "/sso/settings", Summary: "Sign-in settings", Tags: tags},
		func(ctx context.Context, _ *struct{}) (*struct{ Body authSettings }, error) {
			off, err := d.Users.LocalLoginDisabled(ctx)
			if err != nil {
				return nil, d.internalErr("auth settings", err)
			}
			return &struct{ Body authSettings }{authSettings{LocalLoginDisabled: off}}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "update-auth-settings", Method: http.MethodPut, Path: "/sso/settings", Summary: "Change sign-in settings",
		Tags: tags}, func(ctx context.Context, in *struct{ Body authSettings }) (*struct{ Body authSettings }, error) {
		pr, ip := d.actorP(ctx)
		if err := d.OIDC.SetLocalLoginDisabled(ctx, users.Actor(pr.User, ip), in.Body.LocalLoginDisabled); err != nil {
			return nil, d.authErr("auth settings", err)
		}
		return &struct{ Body authSettings }{in.Body}, nil
	})
}

// oidcHandlers are the browser endpoints of the SSO flow (outside the JSON API: they redirect).
func (d *Deps) oidcHandlers(mux *http.ServeMux) {
	mux.HandleFunc("GET /auth/oidc/{provider}/start", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		l := listenerFrom(ctx)
		if !d.ssoHere(ctx, r) {
			http.Redirect(w, r, d.OIDC.BaseURL()+"/login", http.StatusSeeOther)
			return
		}
		q := r.URL.Query()
		purpose := q.Get("purpose")
		if purpose == "" {
			purpose = oidc.PurposeLogin
		}
		p := principalFrom(ctx)
		if purpose == oidc.PurposeTest {
			if err := rbac.Authorize(p, rbac.AuthSettings); err != nil {
				loginErrorRedirect(w, r, err)
				return
			}
		}
		res, err := d.OIDC.Start(ctx, oidc.StartInput{Provider: r.PathValue("provider"), Purpose: purpose, ReturnTo: q.Get("return_to"),
			Principal: p, LinkToken: q.Get("link_token")})
		if err != nil {
			loginErrorRedirect(w, r, err)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: l.oidcCookie(), Value: res.Binding, Path: "/", HttpOnly: true, Secure: l.secure, //nolint:gosec // plain-http loopback only
			SameSite: http.SameSiteLaxMode, MaxAge: int(time.Until(res.ExpiresAt).Seconds())})
		w.Header().Set("Location", res.RedirectURL)
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("GET "+oidc.CallbackPath, func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		l := listenerFrom(ctx)
		q := r.URL.Query()
		binding := ""
		if ck, err := r.Cookie(l.oidcCookie()); err == nil {
			binding = ck.Value
		}
		http.SetCookie(w, &http.Cookie{Name: l.oidcCookie(), Value: "", Path: "/", HttpOnly: true, Secure: l.secure, SameSite: http.SameSiteLaxMode, MaxAge: -1}) //nolint:gosec // plain-http loopback only
		res, err := d.OIDC.Callback(ctx, oidc.CallbackInput{State: q.Get("state"), Code: q.Get("code"), Error: q.Get("error"),
			ErrorDetails: q.Get("error_description"), Binding: binding, Principal: principalFrom(ctx), Meta: d.meta(ctx, r)})
		if err != nil {
			loginErrorRedirect(w, r, err)
			return
		}
		loc := oidc.SanitizeReturnTo(res.ReturnTo)
		switch res.Purpose {
		case oidc.PurposeLogin, oidc.PurposeStepUp:
			c := sessionCookie(l, res.SessionToken)
			http.SetCookie(w, &c)
		case oidc.PurposeTest:
			d.ssoTests.put(principalFrom(ctx).User.ID+"|"+res.ProviderID, res.Test)
			loc = "/settings?tab=sso&test_login=" + url.QueryEscape(res.ProviderID)
		case oidc.PurposeLink:
			if principalFrom(ctx) == nil {
				loc = "/login?linked=1"
			}
		}
		// Set verbatim: http.Redirect would path-clean the sanitized relative target.
		w.Header().Set("Location", loc)
		w.WriteHeader(http.StatusSeeOther)
	})
}

// loginErrorRedirect sends the browser back to the login page with a stable error code (never internals).
func loginErrorRedirect(w http.ResponseWriter, _ *http.Request, err error) {
	code := string(users.KindOf(err))
	if code == "" {
		code = "internal"
	}
	w.Header().Set("Location", "/login?sso_error="+url.QueryEscape(code))
	w.WriteHeader(http.StatusSeeOther)
}
