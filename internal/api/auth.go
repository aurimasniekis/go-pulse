package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/aurimasniekis/go-pulse/internal/oidc"
	"github.com/aurimasniekis/go-pulse/internal/rbac"
	"github.com/aurimasniekis/go-pulse/internal/users"
)

// Me is the signed-in user as the dashboard sees it.
type Me struct {
	User              *users.User       `json:"user"`
	Permissions       []rbac.Permission `json:"permissions"`
	SessionID         string            `json:"session_id"`
	Method            string            `json:"auth_method" enum:"password,oidc,breakglass,token"`
	Provider          string            `json:"auth_provider,omitempty"`
	Listener          string            `json:"listener"`
	CSRFToken         string            `json:"csrf_token,omitempty" doc:"Send as X-Pulse-CSRF on state-changing requests"`
	MFAPending        bool              `json:"mfa_pending" doc:"Enter a TOTP or recovery code (POST /auth/mfa) to finish signing in"`
	MustEnroll        bool              `json:"must_enroll" doc:"Enroll TOTP (POST /account/totp/begin + confirm) to finish signing in"`
	AuthAt            time.Time         `json:"auth_at"`
	ExpiresAt         time.Time         `json:"expires_at"`
	ReauthWindowSecs  int               `json:"reauth_window_seconds"`
	RecoveryCodesLeft int               `json:"recovery_codes_left"`
}

func (d *Deps) me(ctx context.Context, p *users.Principal) *Me {
	m := &Me{User: p.User, Permissions: rbac.Permissions(p.User.Roles), SessionID: p.SessionID, Method: p.Method, Provider: p.Provider,
		Listener: p.Listener, CSRFToken: p.CSRFToken, MFAPending: p.Limited && !p.NeedsEnroll, MustEnroll: p.NeedsEnroll, AuthAt: p.AuthAt,
		ExpiresAt: p.ExpiresAt, ReauthWindowSecs: int(d.ReauthWindow / time.Second)}
	if p.Limited {
		m.Permissions = []rbac.Permission{}
	}
	if p.User.MFAEnrolled {
		m.RecoveryCodesLeft, _ = d.Users.RecoveryCodesLeft(ctx, p.User.ID)
	}
	return m
}

// AuthConfig drives the login page.
type AuthConfig struct {
	LocalLogin        bool                  `json:"local_login"`
	Providers         []oidc.PublicProvider `json:"providers"`
	SSOHere           bool                  `json:"sso_here" doc:"SSO runs on this origin; otherwise use sso_url"`
	SSOURL            string                `json:"sso_url,omitempty" doc:"The dashboard origin SSO redirects to"`
	BootstrapRequired bool                  `json:"bootstrap_required" doc:"No user exists yet: run pulsed admin bootstrap"`
}

type withSession[T any] struct {
	SetCookie []http.Cookie `header:"Set-Cookie"`
	Body      T
}

func (d *Deps) setSession(ctx context.Context, token string) []http.Cookie {
	return []http.Cookie{sessionCookie(listenerFrom(ctx), token)}
}

// ssoHere reports whether the request arrived on the origin SSO redirects to.
func (d *Deps) ssoHere(ctx context.Context, r *http.Request) bool {
	if d.OIDC == nil || r == nil {
		return false
	}
	return d.OIDC.BaseURL() == listenerFrom(ctx).scheme()+"://"+r.Host
}

type reqKey struct{}

// withRequest keeps the *http.Request reachable from Huma handlers (user agent, Host).
func withRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), reqKey{}, r)))
	})
}

func requestFrom(ctx context.Context) *http.Request {
	r, _ := ctx.Value(reqKey{}).(*http.Request)
	return r
}

func registerAuth(api huma.API, d *Deps) {
	tags := []string{"auth"}
	huma.Register(api, huma.Operation{OperationID: "get-auth-config", Method: http.MethodGet, Path: "/auth/config", Summary: "Sign-in options", Tags: tags},
		func(ctx context.Context, _ *struct{}) (*struct{ Body AuthConfig }, error) {
			off, err := d.Users.LocalLoginDisabled(ctx)
			if err != nil {
				return nil, d.internalErr("auth config", err)
			}
			n, err := d.Users.Count(ctx)
			if err != nil {
				return nil, d.internalErr("auth config", err)
			}
			out := &struct{ Body AuthConfig }{AuthConfig{LocalLogin: !off, Providers: []oidc.PublicProvider{}, BootstrapRequired: n == 0}}
			if d.OIDC != nil {
				if out.Body.Providers, err = d.OIDC.EnabledProviders(ctx); err != nil {
					return nil, d.internalErr("auth config", err)
				}
				out.Body.SSOHere = d.ssoHere(ctx, requestFrom(ctx))
				if !out.Body.SSOHere {
					out.Body.SSOURL = d.OIDC.BaseURL()
				}
			}
			return out, nil
		})

	type loginOut struct {
		MFAPending bool `json:"mfa_pending"`
		MustEnroll bool `json:"must_enroll"`
		Me         *Me  `json:"me"`
	}
	huma.Register(api, huma.Operation{OperationID: "login", Method: http.MethodPost, Path: "/auth/login", Summary: "Sign in with email and password", Tags: tags},
		func(ctx context.Context, in *struct {
			Body struct {
				Email    string `json:"email" maxLength:"254"`
				Password string `json:"password" maxLength:"4096"`
			}
		}) (*withSession[loginOut], error) {
			meta := d.meta(ctx, requestFrom(ctx))
			res, err := d.Users.Login(ctx, in.Body.Email, in.Body.Password, meta)
			if err != nil {
				if errors.Is(err, users.ErrInvalidCredentials) && meta.Listener == ListenerPublic && d.OnAuthFailure != nil {
					d.OnAuthFailure(meta.IP, "dashboard")
				}
				return nil, d.authErr("login", err)
			}
			return &withSession[loginOut]{SetCookie: d.setSession(ctx, res.Token),
				Body: loginOut{MFAPending: res.MFAPending, MustEnroll: res.MustEnroll, Me: d.me(ctx, res.Principal)}}, nil
		})

	type codeIn struct {
		Body struct {
			Code string `json:"code" maxLength:"64"`
		}
	}
	huma.Register(api, huma.Operation{OperationID: "verify-mfa", Method: http.MethodPost, Path: "/auth/mfa", Summary: "Finish signing in with a second factor",
		Tags: tags}, func(ctx context.Context, in *codeIn) (*withSession[*Me], error) {
		token, err := d.Users.VerifyMFA(ctx, principalFrom(ctx), in.Body.Code, d.meta(ctx, requestFrom(ctx)))
		if err != nil {
			return nil, d.authErr("verify mfa", err)
		}
		return d.freshSession(ctx, token)
	})

	type logoutOut struct {
		ProviderLogoutURL string `json:"provider_logout_url,omitempty" doc:"Optional RP-initiated logout at the identity provider"`
	}
	huma.Register(api, huma.Operation{OperationID: "logout", Method: http.MethodPost, Path: "/auth/logout", Summary: "Sign out", Tags: tags},
		func(ctx context.Context, _ *struct{}) (*withSession[logoutOut], error) {
			p := principalFrom(ctx)
			if p.Method == users.MethodToken {
				return nil, d.authErr("logout", users.Errf(users.KindPrecondition, "revoke the token instead"))
			}
			if err := d.Users.Logout(ctx, sessionTokenFrom(ctx)); err != nil {
				return nil, d.internalErr("logout", err)
			}
			out := &withSession[logoutOut]{SetCookie: d.setSession(ctx, "")}
			if p.Method == users.MethodOIDC && p.Provider != "" && d.OIDC != nil {
				if u, err := d.OIDC.LogoutURL(ctx, p.Provider, "/login"); err == nil {
					out.Body.ProviderLogoutURL = u
				}
			}
			return out, nil
		})

	huma.Register(api, huma.Operation{OperationID: "reauth", Method: http.MethodPost, Path: "/auth/reauth", Summary: "Confirm it is you (password, and code when enrolled)",
		Tags: tags}, func(ctx context.Context, in *struct {
		Body struct {
			Password string `json:"password" maxLength:"4096"`
			Code     string `json:"code,omitempty" maxLength:"64"`
		}
	}) (*withSession[*Me], error) {
		token, err := d.Users.Reauth(ctx, principalFrom(ctx), in.Body.Password, in.Body.Code, d.meta(ctx, requestFrom(ctx)))
		if err != nil {
			return nil, d.authErr("reauth", err)
		}
		return d.freshSession(ctx, token)
	})
	huma.Register(api, huma.Operation{OperationID: "reauth-totp", Method: http.MethodPost, Path: "/auth/reauth/totp",
		Summary: "Confirm it is you with TOTP (SSO sessions whose provider allows it)", Tags: tags},
		func(ctx context.Context, in *codeIn) (*withSession[*Me], error) {
			if d.OIDC == nil {
				return nil, d.authErr("reauth totp", users.ErrTOTPStepUpNotAllowed)
			}
			token, err := d.OIDC.ReauthTOTP(ctx, principalFrom(ctx), in.Body.Code, d.meta(ctx, requestFrom(ctx)))
			if err != nil {
				return nil, d.authErr("reauth totp", err)
			}
			return d.freshSession(ctx, token)
		})

	type recoverOut struct {
		MustEnroll    bool `json:"must_enroll"`
		PasswordReset *struct {
			Token     string    `json:"token"`
			ExpiresAt time.Time `json:"expires_at"`
		} `json:"password_reset,omitempty" doc:"Set a password you know with this single-use token"`
		Me *Me `json:"me"`
	}
	huma.Register(api, huma.Operation{OperationID: "recover-login", Method: http.MethodPost, Path: "/auth/recover",
		Summary: "Redeem a break-glass recovery token (pulsed admin recover)", Tags: tags}, func(ctx context.Context, in *struct {
		Body struct {
			Token string `json:"token" maxLength:"128"`
		}
	}) (*withSession[recoverOut], error) {
		res, err := d.Users.RecoveryLogin(ctx, in.Body.Token, d.meta(ctx, requestFrom(ctx)))
		if err != nil {
			return nil, d.authErr("recover", err)
		}
		out := &withSession[recoverOut]{SetCookie: d.setSession(ctx, res.Token), Body: recoverOut{MustEnroll: res.Principal.NeedsEnroll, Me: d.me(ctx, res.Principal)}}
		if res.ResetToken != "" {
			out.Body.PasswordReset = &struct {
				Token     string    `json:"token"`
				ExpiresAt time.Time `json:"expires_at"`
			}{res.ResetToken, res.ResetExpiresAt}
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{OperationID: "setup", Method: http.MethodPost, Path: "/auth/setup", Summary: "Create the first administrator with the setup token",
		DefaultStatus: http.StatusCreated, Tags: tags}, func(ctx context.Context, in *struct{ Body users.BootstrapInput }) (*struct{ Body *users.User }, error) {
		u, err := d.Users.Bootstrap(ctx, in.Body, d.meta(ctx, nil).IP)
		if err != nil {
			return nil, d.authErr("setup", err)
		}
		return &struct{ Body *users.User }{u}, nil
	})
	huma.Register(api, huma.Operation{OperationID: "accept-invitation", Method: http.MethodPost, Path: "/auth/invitations/{token}/accept",
		Summary: "Accept an invitation and set a password", DefaultStatus: http.StatusCreated, Tags: tags}, func(ctx context.Context, in *struct {
		Token string `path:"token" maxLength:"128"`
		Body  struct {
			DisplayName string `json:"display_name,omitempty" maxLength:"128"`
			Password    string `json:"password" maxLength:"4096"`
		}
	}) (*struct{ Body *users.User }, error) {
		u, err := d.Users.AcceptInvitation(ctx, in.Token, in.Body.DisplayName, in.Body.Password, d.meta(ctx, nil).IP)
		if err != nil {
			return nil, d.authErr("accept invitation", err)
		}
		return &struct{ Body *users.User }{u}, nil
	})
	huma.Register(api, huma.Operation{OperationID: "reset-password", Method: http.MethodPost, Path: "/auth/reset/{token}",
		Summary: "Set a password with a reset link", DefaultStatus: http.StatusNoContent, Tags: tags}, func(ctx context.Context, in *struct {
		Token string `path:"token" maxLength:"128"`
		Body  struct {
			Password string `json:"password" maxLength:"4096"`
		}
	}) (*struct{}, error) {
		if err := d.Users.ResetPassword(ctx, in.Token, in.Body.Password, d.meta(ctx, nil).IP); err != nil {
			return nil, d.authErr("reset password", err)
		}
		return nil, nil
	})

	huma.Register(api, huma.Operation{OperationID: "get-me", Method: http.MethodGet, Path: "/auth/me", Summary: "The signed-in user", Tags: tags},
		func(ctx context.Context, _ *struct{}) (*struct{ Body *Me }, error) {
			return &struct{ Body *Me }{d.me(ctx, principalFrom(ctx))}, nil
		})
}

// freshSession returns a rotated session token with /me.
func (d *Deps) freshSession(ctx context.Context, token string) (*withSession[*Me], error) {
	p, err := d.Users.Authenticate(ctx, token, listenerFrom(ctx).kind)
	if err != nil {
		return nil, d.authErr("session", err)
	}
	return &withSession[*Me]{SetCookie: d.setSession(ctx, token), Body: d.me(ctx, p)}, nil
}

func registerAccount(api huma.API, d *Deps) {
	tags := []string{"account"}
	huma.Register(api, huma.Operation{OperationID: "list-my-sessions", Method: http.MethodGet, Path: "/account/sessions", Summary: "My active sessions", Tags: tags},
		func(ctx context.Context, _ *struct{}) (*struct{ Body []users.SessionInfo }, error) {
			p := principalFrom(ctx)
			v, err := d.Users.ListSessions(ctx, p.User.ID, p.SessionID)
			if err != nil {
				return nil, d.internalErr("list sessions", err)
			}
			return &struct{ Body []users.SessionInfo }{v}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "revoke-my-session", Method: http.MethodDelete, Path: "/account/sessions/{id}", Summary: "Sign out one of my sessions",
		DefaultStatus: http.StatusNoContent, Tags: tags}, func(ctx context.Context, in *struct {
		ID string `path:"id" maxLength:"64"`
	}) (*struct{}, error) {
		if err := d.Users.RevokeSession(ctx, principalFrom(ctx).User.ID, in.ID, "revoked_by_owner"); err != nil {
			return nil, d.authErr("revoke session", err)
		}
		return nil, nil
	})
	huma.Register(api, huma.Operation{OperationID: "change-my-password", Method: http.MethodPost, Path: "/account/password", Summary: "Change my password",
		DefaultStatus: http.StatusNoContent, Tags: tags}, func(ctx context.Context, in *struct {
		Body struct {
			Current string `json:"current_password" maxLength:"4096"`
			New     string `json:"new_password" maxLength:"4096"`
		}
	}) (*struct{}, error) {
		if err := d.Users.ChangePassword(ctx, principalFrom(ctx), in.Body.Current, in.Body.New, d.meta(ctx, nil).IP); err != nil {
			return nil, d.authErr("change password", err)
		}
		return nil, nil
	})
	huma.Register(api, huma.Operation{OperationID: "begin-totp", Method: http.MethodPost, Path: "/account/totp/begin",
		Summary: "Start TOTP enrollment (secret shown once)", Tags: tags}, func(ctx context.Context, _ *struct{}) (*struct{ Body *users.TOTPEnrollment }, error) {
		e, err := d.Users.BeginTOTP(ctx, principalFrom(ctx))
		if err != nil {
			return nil, d.authErr("begin totp", err)
		}
		return &struct{ Body *users.TOTPEnrollment }{e}, nil
	})
	type confirmOut struct {
		Codes []string `json:"recovery_codes" doc:"Single-use recovery codes, shown once"`
	}
	huma.Register(api, huma.Operation{OperationID: "confirm-totp", Method: http.MethodPost, Path: "/account/totp/confirm", Summary: "Finish TOTP enrollment",
		Tags: tags}, func(ctx context.Context, in *struct {
		Body struct {
			Code string `json:"code" maxLength:"16"`
		}
	}) (*withSession[confirmOut], error) {
		codes, token, err := d.Users.ConfirmTOTP(ctx, principalFrom(ctx), in.Body.Code, d.meta(ctx, requestFrom(ctx)))
		if err != nil {
			return nil, d.authErr("confirm totp", err)
		}
		out := &withSession[confirmOut]{Body: confirmOut{Codes: codes}}
		if token != "" {
			out.SetCookie = d.setSession(ctx, token)
		}
		return out, nil
	})
	huma.Register(api, huma.Operation{OperationID: "disable-totp", Method: http.MethodDelete, Path: "/account/totp", Summary: "Remove my second factor",
		DefaultStatus: http.StatusNoContent, Tags: tags}, func(ctx context.Context, _ *struct{}) (*struct{}, error) {
		if err := d.Users.DisableTOTP(ctx, principalFrom(ctx), d.meta(ctx, nil).IP); err != nil {
			return nil, d.authErr("disable totp", err)
		}
		return nil, nil
	})
	huma.Register(api, huma.Operation{OperationID: "regenerate-recovery-codes", Method: http.MethodPost, Path: "/account/recovery-codes",
		Summary: "Replace my recovery codes (shown once)", Tags: tags}, func(ctx context.Context, _ *struct{}) (*struct{ Body confirmOut }, error) {
		codes, err := d.Users.RegenerateRecoveryCodes(ctx, principalFrom(ctx), d.meta(ctx, nil).IP)
		if err != nil {
			return nil, d.authErr("recovery codes", err)
		}
		return &struct{ Body confirmOut }{confirmOut{Codes: codes}}, nil
	})
	huma.Register(api, huma.Operation{OperationID: "list-my-identities", Method: http.MethodGet, Path: "/account/identities", Summary: "My linked SSO identities",
		Tags: tags}, func(ctx context.Context, _ *struct{}) (*struct{ Body []oidc.Identity }, error) {
		if d.OIDC == nil {
			return &struct{ Body []oidc.Identity }{[]oidc.Identity{}}, nil
		}
		v, err := d.OIDC.Identities(ctx, principalFrom(ctx).User.ID)
		if err != nil {
			return nil, d.internalErr("identities", err)
		}
		return &struct{ Body []oidc.Identity }{v}, nil
	})
	huma.Register(api, huma.Operation{OperationID: "list-my-tokens", Method: http.MethodGet, Path: "/account/tokens", Summary: "My personal access tokens",
		Tags: tags}, func(ctx context.Context, _ *struct{}) (*struct{ Body []*users.PAT }, error) {
		v, err := d.Users.ListPATs(ctx, principalFrom(ctx).User.ID)
		if err != nil {
			return nil, d.internalErr("list tokens", err)
		}
		return &struct{ Body []*users.PAT }{v}, nil
	})
	type patOut struct {
		Token  *users.PAT `json:"token"`
		Secret string     `json:"secret" doc:"Shown once"`
	}
	huma.Register(api, huma.Operation{OperationID: "create-my-token", Method: http.MethodPost, Path: "/account/tokens",
		Summary: "Create a personal access token for the pulse CLI (loopback listener only)", DefaultStatus: http.StatusCreated, Tags: tags},
		func(ctx context.Context, in *struct {
			Body struct {
				Name string `json:"name" maxLength:"64"`
				Role string `json:"role" enum:"administrator,operator,auditor,viewer"`
				Days int    `json:"days,omitempty" minimum:"1" maximum:"366" doc:"Lifetime (default 90)"`
			}
		}) (*struct{ Body patOut }, error) {
			ttl := time.Duration(in.Body.Days) * 24 * time.Hour
			t, secret, err := d.Users.CreatePAT(ctx, principalFrom(ctx), in.Body.Name, in.Body.Role, ttl, d.meta(ctx, nil).IP)
			if err != nil {
				return nil, d.authErr("create token", err)
			}
			return &struct{ Body patOut }{patOut{Token: t, Secret: secret}}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "revoke-my-token", Method: http.MethodDelete, Path: "/account/tokens/{id}", Summary: "Revoke one of my tokens",
		DefaultStatus: http.StatusNoContent, Tags: tags}, func(ctx context.Context, in *struct {
		ID string `path:"id" maxLength:"64"`
	}) (*struct{}, error) {
		p := principalFrom(ctx)
		if err := d.Users.RevokePAT(ctx, users.Actor(p.User, d.meta(ctx, nil).IP), p.User.ID, in.ID); err != nil {
			return nil, d.authErr("revoke token", err)
		}
		return nil, nil
	})
}
