package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/aurimasniekis/go-pulse/internal/audit"
	"github.com/aurimasniekis/go-pulse/internal/clients"
	"github.com/aurimasniekis/go-pulse/internal/natsauth"
	"github.com/aurimasniekis/go-pulse/internal/sysapi"
	"github.com/aurimasniekis/go-pulse/internal/tokens"
	"github.com/aurimasniekis/go-pulse/internal/users"
)

// ClientDTO is a client plus its live connection count.
type ClientDTO struct {
	clients.Client
	Connections int  `json:"connections"`
	ExpiresSoon bool `json:"expires_soon" doc:"Expires within 30 days"`
}

// Issued is returned once when credentials are created or rotated (PL-AUTH-004).
type Issued struct {
	Client ClientDTO `json:"client"`
	Creds  string    `json:"creds" doc:"The .creds file. Shown once; pulsed does not keep the seed."`
}

// ClientInput creates a client.
type ClientInput struct {
	Name          string           `json:"name" pattern:"^[a-z0-9][a-z0-9_.-]{0,63}$"`
	Description   string           `json:"description,omitempty" maxLength:"512"`
	Tags          []string         `json:"tags,omitempty" maxItems:"16"`
	Grants        []natsauth.Grant `json:"grants,omitempty" maxItems:"64"`
	ExpiresInDays int              `json:"expires_in_days,omitempty" minimum:"0" maximum:"3650" doc:"0 = 365 days"`
}

// ConnDTO is one live NATS connection of a client (PL-AUTH-010).
type ConnDTO struct {
	CID        uint64    `json:"cid"`
	IP         string    `json:"ip"`
	Port       int       `json:"port"`
	Start      time.Time `json:"start"`
	LastActive time.Time `json:"last_active"`
	RTT        string    `json:"rtt"`
	Uptime     string    `json:"uptime"`
	Subs       uint32    `json:"subscriptions"`
	InMsgs     int64     `json:"in_msgs"`
	OutMsgs    int64     `json:"out_msgs"`
	InBytes    int64     `json:"in_bytes"`
	OutBytes   int64     `json:"out_bytes"`
	Name       string    `json:"name"`
	Lang       string    `json:"lang"`
	Version    string    `json:"version"`
	Kind       string    `json:"kind" doc:"Client or WebSocket"`
	Account    string    `json:"account,omitempty" doc:"Account name (APP, SYS); set on the System page"`
	Pending    int       `json:"pending_bytes"`
}

func connDTO(c sysapi.ConnInfo) ConnDTO {
	return ConnDTO{CID: c.Cid, IP: c.IP, Port: c.Port, Start: c.Start, LastActive: c.LastActivity, RTT: c.RTT, Uptime: c.Uptime,
		Subs: c.NumSubs, InMsgs: c.InMsgs, OutMsgs: c.OutMsgs, InBytes: c.InBytes, OutBytes: c.OutBytes, Name: c.Name, Lang: c.Lang,
		Version: c.Version, Kind: c.Type, Pending: c.Pending}
}

func (d *Deps) clientDTO(ctx context.Context, c *clients.Client, counts map[string]int) ClientDTO {
	dto := ClientDTO{Client: *c, Connections: counts[c.UserKey]}
	dto.ExpiresSoon = c.Status == clients.StatusActive && time.Until(c.ExpiresAt) < 30*24*time.Hour
	return dto
}

// connCounts maps user keys to live connection counts (best effort; empty when NATS is unreachable).
func (d *Deps) connCounts(ctx context.Context) map[string]int {
	out := map[string]int{}
	if d.Sys == nil {
		return out
	}
	cz, err := d.Sys.Connz(ctx, sysapi.ConnzOptions{Auth: true, Limit: 4096})
	if err != nil {
		return out
	}
	for _, c := range cz.Conns {
		out[c.AuthorizedUser]++
	}
	return out
}

func clientErr(d *Deps, op string, err error) error {
	switch {
	case errors.Is(err, clients.ErrNotFound):
		return huma.Error404NotFound("client not found")
	case errors.Is(err, clients.ErrConflict):
		return huma.Error409Conflict("client was changed by someone else; reload and retry")
	case errors.Is(err, clients.ErrRevoked):
		return huma.Error409Conflict("client is revoked")
	case errors.Is(err, clients.ErrNotRevoked):
		return preconditionErr(err.Error())
	case errors.Is(err, clients.ErrInvalid):
		return huma.Error422UnprocessableEntity(err.Error())
	default:
		return d.internalErr(op, err)
	}
}

// preconditionErr is a 412 problem with the "precondition" code, as the users and SSO handlers return.
func preconditionErr(msg string) error {
	return toProblem(users.Errf(users.KindPrecondition, "%s", msg))
}

type clientPath struct {
	ID string `path:"id" pattern:"^[a-z0-9]{6,32}$"`
}

func registerClients(api huma.API, d *Deps) {
	tags := []string{"clients"}
	huma.Register(api, huma.Operation{OperationID: "list-clients", Method: http.MethodGet, Path: "/clients", Summary: "List clients", Tags: tags},
		func(ctx context.Context, _ *struct{}) (*struct{ Body []ClientDTO }, error) {
			cs, err := d.Clients.List(ctx)
			if err != nil {
				return nil, d.internalErr("list-clients", err)
			}
			counts := d.connCounts(ctx)
			out := &struct{ Body []ClientDTO }{Body: make([]ClientDTO, 0, len(cs))}
			for _, c := range cs {
				out.Body = append(out.Body, d.clientDTO(ctx, c, counts))
			}
			return out, nil
		})
	huma.Register(api, huma.Operation{OperationID: "create-client", Method: http.MethodPost, Path: "/clients", Summary: "Create a client and its credentials",
		Tags: tags, DefaultStatus: http.StatusCreated},
		func(ctx context.Context, in *struct{ Body ClientInput }) (*struct{ Body Issued }, error) {
			c, creds, err := d.Clients.Create(ctx, clients.CreateInput{Name: in.Body.Name, Description: in.Body.Description, Tags: in.Body.Tags,
				Grants: in.Body.Grants, ExpiresIn: days(in.Body.ExpiresInDays)}, d.actor(ctx))
			if err != nil {
				return nil, clientErr(d, "create-client", err)
			}
			return &struct{ Body Issued }{Issued{Client: d.clientDTO(ctx, c, nil), Creds: string(creds)}}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "get-client", Method: http.MethodGet, Path: "/clients/{id}", Summary: "Get a client", Tags: tags},
		func(ctx context.Context, in *clientPath) (*struct{ Body ClientDTO }, error) {
			c, err := d.Clients.Get(ctx, in.ID)
			if err != nil {
				return nil, clientErr(d, "get-client", err)
			}
			return &struct{ Body ClientDTO }{d.clientDTO(ctx, c, d.connCounts(ctx))}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "update-client", Method: http.MethodPut, Path: "/clients/{id}", Summary: "Update name, description and tags", Tags: tags},
		func(ctx context.Context, in *struct {
			ID   string `path:"id" pattern:"^[a-z0-9]{6,32}$"`
			Body struct {
				Name        string   `json:"name" pattern:"^[a-z0-9][a-z0-9_.-]{0,63}$"`
				Description string   `json:"description,omitempty" maxLength:"512"`
				Tags        []string `json:"tags,omitempty" maxItems:"16"`
				Version     int64    `json:"version"`
			}
		}) (*struct{ Body ClientDTO }, error) {
			c, err := d.Clients.UpdateMeta(ctx, in.ID, in.Body.Name, in.Body.Description, in.Body.Tags, in.Body.Version, d.actor(ctx))
			if err != nil {
				return nil, clientErr(d, "update-client", err)
			}
			return &struct{ Body ClientDTO }{d.clientDTO(ctx, c, d.connCounts(ctx))}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "update-client-grants", Method: http.MethodPut, Path: "/clients/{id}/grants", Summary: "Replace grants",
		Description: "Takes effect without new credentials; NATS reconnects the client's live connections with the new permissions.", Tags: tags},
		func(ctx context.Context, in *struct {
			ID   string `path:"id" pattern:"^[a-z0-9]{6,32}$"`
			Body struct {
				Grants  []natsauth.Grant `json:"grants" maxItems:"64"`
				Version int64            `json:"version"`
			}
		}) (*struct{ Body ClientDTO }, error) {
			c, err := d.Clients.UpdateGrants(ctx, in.ID, in.Body.Grants, in.Body.Version, d.actor(ctx))
			if err != nil {
				return nil, clientErr(d, "update-grants", err)
			}
			return &struct{ Body ClientDTO }{d.clientDTO(ctx, c, d.connCounts(ctx))}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "rotate-client", Method: http.MethodPost, Path: "/clients/{id}/rotate", Summary: "Issue new credentials, revoke the old ones", Tags: tags},
		func(ctx context.Context, in *struct {
			ID   string `path:"id" pattern:"^[a-z0-9]{6,32}$"`
			Body struct {
				ExpiresInDays int `json:"expires_in_days,omitempty" minimum:"0" maximum:"3650"`
			}
		}) (*struct{ Body Issued }, error) {
			c, creds, err := d.Clients.Rotate(ctx, in.ID, days(in.Body.ExpiresInDays), d.actor(ctx))
			if err != nil {
				return nil, clientErr(d, "rotate-client", err)
			}
			return &struct{ Body Issued }{Issued{Client: d.clientDTO(ctx, c, nil), Creds: string(creds)}}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "revoke-client", Method: http.MethodPost, Path: "/clients/{id}/revoke", Summary: "Revoke a client",
		Description: "Permanent. Its connections are closed within seconds.", Tags: tags, DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, in *clientPath) (*struct{}, error) {
			if err := d.Clients.Revoke(ctx, in.ID, d.actor(ctx)); err != nil {
				return nil, clientErr(d, "revoke-client", err)
			}
			return &struct{}{}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "delete-client", Method: http.MethodDelete, Path: "/clients/{id}", Summary: "Delete a revoked client",
		Description: "Removes the record so its name can be reused; the revocation of its credentials stays in force.", Tags: tags,
		DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, in *clientPath) (*struct{}, error) {
			if err := d.Clients.Delete(ctx, in.ID, d.actor(ctx)); err != nil {
				return nil, clientErr(d, "delete-client", err)
			}
			return &struct{}{}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "kick-client", Method: http.MethodPost, Path: "/clients/{id}/kick", Summary: "Disconnect live connections", Tags: tags},
		func(ctx context.Context, in *clientPath) (*struct {
			Body struct {
				Kicked int `json:"kicked"`
			}
		}, error) {
			n, err := d.Clients.Kick(ctx, in.ID, d.actor(ctx))
			if err != nil {
				return nil, clientErr(d, "kick-client", err)
			}
			out := &struct {
				Body struct {
					Kicked int `json:"kicked"`
				}
			}{}
			out.Body.Kicked = n
			return out, nil
		})
	huma.Register(api, huma.Operation{OperationID: "client-connections", Method: http.MethodGet, Path: "/clients/{id}/connections", Summary: "Live connections", Tags: tags},
		func(ctx context.Context, in *clientPath) (*struct{ Body []ConnDTO }, error) {
			c, err := d.Clients.Get(ctx, in.ID)
			if err != nil {
				return nil, clientErr(d, "connections", err)
			}
			conns, err := d.Clients.Connections(ctx, c.UserKey)
			if err != nil {
				return nil, d.internalErr("connections", err)
			}
			out := &struct{ Body []ConnDTO }{Body: make([]ConnDTO, 0, len(conns))}
			for _, cn := range conns {
				out.Body = append(out.Body, connDTO(cn))
			}
			return out, nil
		})
}

func days(n int) time.Duration { return time.Duration(n) * 24 * time.Hour }

// TokenInput creates an HTTP token.
type TokenInput struct {
	Name          string           `json:"name" pattern:"^[a-z0-9][a-z0-9_.-]{0,63}$"`
	Description   string           `json:"description,omitempty" maxLength:"512"`
	Grants        []natsauth.Grant `json:"grants,omitempty" maxItems:"64" doc:"publish, subscribe or request"`
	CIDRs         []string         `json:"cidrs,omitempty" maxItems:"32"`
	ExpiresInDays int              `json:"expires_in_days,omitempty" minimum:"0" maximum:"3650"`
}

func registerTokens(api huma.API, d *Deps) {
	tags := []string{"tokens"}
	huma.Register(api, huma.Operation{OperationID: "list-tokens", Method: http.MethodGet, Path: "/tokens", Summary: "List HTTP tokens", Tags: tags},
		func(ctx context.Context, _ *struct{}) (*struct{ Body []*tokens.Token }, error) {
			ts, err := d.Tokens.List(ctx)
			if err != nil {
				return nil, d.internalErr("list-tokens", err)
			}
			return &struct{ Body []*tokens.Token }{ts}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "create-token", Method: http.MethodPost, Path: "/tokens", Summary: "Create an HTTP token",
		Description: "The secret is returned once and stored only as a hash.", Tags: tags, DefaultStatus: http.StatusCreated},
		func(ctx context.Context, in *struct{ Body TokenInput }) (*struct {
			Body struct {
				Token  *tokens.Token `json:"token"`
				Secret string        `json:"secret"`
			}
		}, error) {
			t, secret, err := d.Tokens.Create(ctx, tokens.CreateInput{Name: in.Body.Name, Description: in.Body.Description, Grants: in.Body.Grants,
				CIDRs: in.Body.CIDRs, ExpiresIn: days(in.Body.ExpiresInDays)}, d.actor(ctx))
			if errors.Is(err, tokens.ErrInvalid) {
				return nil, huma.Error422UnprocessableEntity(err.Error())
			}
			if err != nil {
				return nil, d.internalErr("create-token", err)
			}
			out := &struct {
				Body struct {
					Token  *tokens.Token `json:"token"`
					Secret string        `json:"secret"`
				}
			}{}
			out.Body.Token, out.Body.Secret = t, secret
			return out, nil
		})
	huma.Register(api, huma.Operation{OperationID: "revoke-token", Method: http.MethodPost, Path: "/tokens/{id}/revoke", Summary: "Revoke an HTTP token",
		Tags: tags, DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, in *struct {
			ID string `path:"id" pattern:"^[a-z0-9]{6,32}$"`
		}) (*struct{}, error) {
			err := d.Tokens.Revoke(ctx, in.ID, d.actor(ctx))
			if errors.Is(err, tokens.ErrNotFound) {
				return nil, huma.Error404NotFound("token not found")
			}
			if err != nil {
				return nil, d.internalErr("revoke-token", err)
			}
			return &struct{}{}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "delete-token", Method: http.MethodDelete, Path: "/tokens/{id}", Summary: "Delete a revoked or expired HTTP token",
		Description: "Removes the record so its name can be reused.", Tags: tags, DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, in *struct {
			ID string `path:"id" pattern:"^[a-z0-9]{6,32}$"`
		}) (*struct{}, error) {
			err := d.Tokens.Delete(ctx, in.ID, d.actor(ctx))
			switch {
			case errors.Is(err, tokens.ErrNotFound):
				return nil, huma.Error404NotFound("token not found")
			case errors.Is(err, tokens.ErrActive):
				return nil, preconditionErr(err.Error())
			case err != nil:
				return nil, d.internalErr("delete-token", err)
			}
			return &struct{}{}, nil
		})
}

// AuditPage is a page of audit entries, newest first.
type AuditPage struct {
	Entries []audit.Entry `json:"entries"`
	Next    int64         `json:"next,omitempty" doc:"Pass as before_id for the next page"`
}

func registerAudit(api huma.API, d *Deps) {
	huma.Register(api, huma.Operation{OperationID: "list-audit", Method: http.MethodGet, Path: "/audit", Summary: "Audit log", Tags: []string{"audit"}},
		func(ctx context.Context, in *struct {
			Action   string    `query:"action" maxLength:"64" doc:"Exact action, or a prefix ending in '.' (e.g. client.)"`
			Actor    string    `query:"actor" maxLength:"128"`
			Target   string    `query:"target" maxLength:"256"`
			Result   string    `query:"result" enum:"ok,denied,error,"`
			Since    time.Time `query:"since"`
			Until    time.Time `query:"until"`
			BeforeID int64     `query:"before_id" minimum:"0"`
			Limit    int       `query:"limit" default:"100" minimum:"1" maximum:"500"`
		}) (*struct{ Body AuditPage }, error) {
			es, err := audit.List(ctx, d.DB, audit.Filter{Action: in.Action, Actor: in.Actor, Target: in.Target, Result: in.Result,
				Since: in.Since, Until: in.Until, BeforeID: in.BeforeID, Limit: in.Limit})
			if err != nil {
				return nil, d.internalErr("audit", err)
			}
			p := AuditPage{Entries: es}
			if len(es) == in.Limit {
				p.Next = es[len(es)-1].ID
			}
			return &struct{ Body AuditPage }{p}, nil
		})
}
