package api

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/aurimasniekis/go-pulse/internal/oidc"
	"github.com/aurimasniekis/go-pulse/internal/users"
)

// OneTimeLink is a single-use link shown once (invitation, password reset, SSO link invitation).
type OneTimeLink struct {
	Token     string    `json:"token"`
	URL       string    `json:"url" doc:"Path on the dashboard origin"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (d *Deps) actorP(ctx context.Context) (*users.Principal, string) {
	return principalFrom(ctx), d.meta(ctx, nil).IP
}

func registerUsers(api huma.API, d *Deps) {
	tags := []string{"users"}
	type idPath struct {
		ID string `path:"id" maxLength:"64"`
	}
	huma.Register(api, huma.Operation{OperationID: "list-users", Method: http.MethodGet, Path: "/users", Summary: "List users", Tags: tags},
		func(ctx context.Context, _ *struct{}) (*struct{ Body []*users.User }, error) {
			v, err := d.Users.List(ctx)
			if err != nil {
				return nil, d.internalErr("list users", err)
			}
			return &struct{ Body []*users.User }{v}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "get-user", Method: http.MethodGet, Path: "/users/{id}", Summary: "Get a user", Tags: tags},
		func(ctx context.Context, in *idPath) (*struct{ Body *users.User }, error) {
			u, err := d.Users.Get(ctx, in.ID)
			if err != nil {
				return nil, d.authErr("get user", err)
			}
			return &struct{ Body *users.User }{u}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "create-user", Method: http.MethodPost, Path: "/users",
		Summary: "Create a user (they set a password through a reset link, or sign in with SSO)", DefaultStatus: http.StatusCreated, Tags: tags},
		func(ctx context.Context, in *struct{ Body users.CreateInput }) (*struct{ Body *users.User }, error) {
			p, ip := d.actorP(ctx)
			u, err := d.Users.Create(ctx, users.Actor(p.User, ip), in.Body)
			if err != nil {
				return nil, d.authErr("create user", err)
			}
			return &struct{ Body *users.User }{u}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "update-user", Method: http.MethodPatch, Path: "/users/{id}", Summary: "Change a user's name, roles or status",
		Tags: tags}, func(ctx context.Context, in *struct {
		ID   string `path:"id" maxLength:"64"`
		Body users.UpdateInput
	}) (*struct{ Body *users.User }, error) {
		p, ip := d.actorP(ctx)
		if in.Body.Enabled != nil && !*in.Body.Enabled && in.ID == p.User.ID {
			return nil, d.authErr("update user", users.Errf(users.KindPrecondition, "you cannot disable your own account"))
		}
		u, err := d.Users.Update(ctx, users.Actor(p.User, ip), in.ID, in.Body)
		if err != nil {
			return nil, d.authErr("update user", err)
		}
		return &struct{ Body *users.User }{u}, nil
	})
	huma.Register(api, huma.Operation{OperationID: "delete-user", Method: http.MethodDelete, Path: "/users/{id}", Summary: "Delete a user",
		DefaultStatus: http.StatusNoContent, Tags: tags}, func(ctx context.Context, in *idPath) (*struct{}, error) {
		p, ip := d.actorP(ctx)
		if in.ID == p.User.ID {
			return nil, d.authErr("delete user", users.Errf(users.KindPrecondition, "you cannot delete your own account"))
		}
		if err := d.Users.Delete(ctx, users.Actor(p.User, ip), in.ID); err != nil {
			return nil, d.authErr("delete user", err)
		}
		return nil, nil
	})
	huma.Register(api, huma.Operation{OperationID: "issue-reset-link", Method: http.MethodPost, Path: "/users/{id}/reset-link",
		Summary: "Issue a single-use password link (shown once)", Tags: tags}, func(ctx context.Context, in *idPath) (*struct{ Body OneTimeLink }, error) {
		p, ip := d.actorP(ctx)
		tok, exp, err := d.Users.IssueResetToken(ctx, users.Actor(p.User, ip), in.ID)
		if err != nil {
			return nil, d.authErr("reset link", err)
		}
		return &struct{ Body OneTimeLink }{OneTimeLink{Token: tok, URL: "/reset/" + tok, ExpiresAt: exp}}, nil
	})
	huma.Register(api, huma.Operation{OperationID: "list-user-sessions", Method: http.MethodGet, Path: "/users/{id}/sessions", Summary: "A user's sessions",
		Tags: tags}, func(ctx context.Context, in *idPath) (*struct{ Body []users.SessionInfo }, error) {
		v, err := d.Users.ListSessions(ctx, in.ID, principalFrom(ctx).SessionID)
		if err != nil {
			return nil, d.internalErr("list sessions", err)
		}
		return &struct{ Body []users.SessionInfo }{v}, nil
	})
	huma.Register(api, huma.Operation{OperationID: "revoke-user-sessions", Method: http.MethodDelete, Path: "/users/{id}/sessions",
		Summary: "Sign a user out everywhere", DefaultStatus: http.StatusNoContent, Tags: tags}, func(ctx context.Context, in *idPath) (*struct{}, error) {
		if err := d.Users.RevokeAllSessions(ctx, in.ID, "revoked_by_admin"); err != nil {
			return nil, d.internalErr("revoke sessions", err)
		}
		return nil, nil
	})
	huma.Register(api, huma.Operation{OperationID: "clear-user-throttle", Method: http.MethodPost, Path: "/users/{id}/clear-throttle",
		Summary: "Clear a user's sign-in throttling", DefaultStatus: http.StatusNoContent, Tags: tags}, func(ctx context.Context, in *idPath) (*struct{}, error) {
		p, ip := d.actorP(ctx)
		if err := d.Users.ClearThrottle(ctx, users.Actor(p.User, ip), in.ID); err != nil {
			return nil, d.authErr("clear throttle", err)
		}
		return nil, nil
	})
	huma.Register(api, huma.Operation{OperationID: "list-user-tokens", Method: http.MethodGet, Path: "/users/{id}/tokens", Summary: "A user's personal access tokens",
		Tags: tags}, func(ctx context.Context, in *idPath) (*struct{ Body []*users.PAT }, error) {
		v, err := d.Users.ListPATs(ctx, in.ID)
		if err != nil {
			return nil, d.internalErr("list tokens", err)
		}
		return &struct{ Body []*users.PAT }{v}, nil
	})
	huma.Register(api, huma.Operation{OperationID: "revoke-user-token", Method: http.MethodDelete, Path: "/users/{id}/tokens/{token}",
		Summary: "Revoke a user's personal access token", DefaultStatus: http.StatusNoContent, Tags: tags}, func(ctx context.Context, in *struct {
		ID    string `path:"id" maxLength:"64"`
		Token string `path:"token" maxLength:"64"`
	}) (*struct{}, error) {
		p, ip := d.actorP(ctx)
		if err := d.Users.RevokePAT(ctx, users.Actor(p.User, ip), in.ID, in.Token); err != nil {
			return nil, d.authErr("revoke token", err)
		}
		return nil, nil
	})

	// SSO identities of a user
	huma.Register(api, huma.Operation{OperationID: "list-user-identities", Method: http.MethodGet, Path: "/users/{id}/identities",
		Summary: "A user's linked SSO identities", Tags: tags}, func(ctx context.Context, in *idPath) (*struct{ Body []oidc.Identity }, error) {
		v, err := d.OIDC.Identities(ctx, in.ID)
		if err != nil {
			return nil, d.internalErr("identities", err)
		}
		return &struct{ Body []oidc.Identity }{v}, nil
	})
	huma.Register(api, huma.Operation{OperationID: "link-user-identity", Method: http.MethodPost, Path: "/users/{id}/identities",
		Summary: "Link an expected SSO identity (provider + subject)", DefaultStatus: http.StatusNoContent, Tags: tags}, func(ctx context.Context, in *struct {
		ID   string `path:"id" maxLength:"64"`
		Body struct {
			ProviderID string `json:"provider_id" maxLength:"64"`
			Subject    string `json:"subject" maxLength:"255"`
		}
	}) (*struct{}, error) {
		p, ip := d.actorP(ctx)
		if err := d.OIDC.LinkIdentity(ctx, users.Actor(p.User, ip), in.ID, in.Body.ProviderID, in.Body.Subject); err != nil {
			return nil, d.authErr("link identity", err)
		}
		return nil, nil
	})
	huma.Register(api, huma.Operation{OperationID: "unlink-user-identity", Method: http.MethodDelete, Path: "/users/{id}/identities",
		Summary: "Unlink an SSO identity (signs the user out)", DefaultStatus: http.StatusNoContent, Tags: tags}, func(ctx context.Context, in *struct {
		ID      string `path:"id" maxLength:"64"`
		Issuer  string `query:"issuer" maxLength:"512"`
		Subject string `query:"subject" maxLength:"255"`
	}) (*struct{}, error) {
		p, ip := d.actorP(ctx)
		if err := d.OIDC.UnlinkIdentity(ctx, users.Actor(p.User, ip), in.ID, in.Issuer, in.Subject); err != nil {
			return nil, d.authErr("unlink identity", err)
		}
		return nil, nil
	})
	huma.Register(api, huma.Operation{OperationID: "issue-link-invite", Method: http.MethodPost, Path: "/users/{id}/link-invite",
		Summary: "Issue a single-use link that binds the next SSO login to this user", Tags: tags}, func(ctx context.Context, in *struct {
		ID   string `path:"id" maxLength:"64"`
		Body struct {
			ProviderID string `json:"provider_id" maxLength:"64"`
		}
	}) (*struct{ Body OneTimeLink }, error) {
		p, ip := d.actorP(ctx)
		prov, err := d.OIDC.GetProvider(ctx, in.Body.ProviderID)
		if err != nil {
			return nil, d.authErr("link invite", err)
		}
		tok, exp, err := d.OIDC.IssueLinkInvite(ctx, users.Actor(p.User, ip), in.ID, prov.ID)
		if err != nil {
			return nil, d.authErr("link invite", err)
		}
		u := "/auth/oidc/" + url.PathEscape(prov.Slug) + "/start?purpose=link&link_token=" + url.QueryEscape(tok)
		return &struct{ Body OneTimeLink }{OneTimeLink{Token: tok, URL: u, ExpiresAt: exp}}, nil
	})

	// invitations
	huma.Register(api, huma.Operation{OperationID: "list-invitations", Method: http.MethodGet, Path: "/invitations", Summary: "Pending invitations", Tags: tags},
		func(ctx context.Context, _ *struct{}) (*struct{ Body []users.Invitation }, error) {
			v, err := d.Users.ListInvitations(ctx)
			if err != nil {
				return nil, d.internalErr("invitations", err)
			}
			return &struct{ Body []users.Invitation }{v}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "create-invitation", Method: http.MethodPost, Path: "/invitations",
		Summary: "Invite someone by email with preset roles (link shown once)", Tags: tags}, func(ctx context.Context, in *struct {
		Body struct {
			Email string   `json:"email" maxLength:"254"`
			Roles []string `json:"roles"`
		}
	}) (*struct{ Body OneTimeLink }, error) {
		p, ip := d.actorP(ctx)
		tok, exp, err := d.Users.Invite(ctx, users.Actor(p.User, ip), in.Body.Email, in.Body.Roles)
		if err != nil {
			return nil, d.authErr("invite", err)
		}
		return &struct{ Body OneTimeLink }{OneTimeLink{Token: tok, URL: "/invite/" + tok, ExpiresAt: exp}}, nil
	})
	huma.Register(api, huma.Operation{OperationID: "revoke-invitation", Method: http.MethodDelete, Path: "/invitations/{id}", Summary: "Revoke an invitation",
		DefaultStatus: http.StatusNoContent, Tags: tags}, func(ctx context.Context, in *struct {
		ID string `path:"id" maxLength:"128"`
	}) (*struct{}, error) {
		p, ip := d.actorP(ctx)
		if err := d.Users.RevokeInvitation(ctx, users.Actor(p.User, ip), in.ID); err != nil {
			return nil, d.authErr("revoke invitation", err)
		}
		return nil, nil
	})
}
