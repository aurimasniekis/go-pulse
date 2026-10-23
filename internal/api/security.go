package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/aurimasniekis/go-pulse/internal/ban"
)

// EdgeInfo describes the public listener's protections for the Security and System pages.
type EdgeInfo struct {
	TLS             bool      `json:"tls"`
	AOP             bool      `json:"aop" doc:"Authenticated Origin Pulls required"`
	OriginNotAfter  time.Time `json:"origin_not_after,omitzero"`
	CloudflareCount int       `json:"cloudflare_ranges"`
	CloudflareAt    time.Time `json:"cloudflare_updated"`
	BanAgent        bool      `json:"ban_feed" doc:"The banagent feed is served"`
	CFListSync      bool      `json:"cloudflare_list_sync"`
	NeverBan        []string  `json:"never_ban" doc:"Static never-ban networks from configuration"`
	// Refresh returns live values (origin expiry, ranges) at request time.
	Refresh func(*EdgeInfo) `json:"-"`
}

// SecurityOverview is the Security page payload.
type SecurityOverview struct {
	Edge      EdgeInfo         `json:"edge"`
	Active    []ban.Ban        `json:"active"`
	History   []ban.Ban        `json:"history" doc:"Expired or lifted bans from the last 30 days"`
	Failures  []ban.Failure    `json:"failures" doc:"Recent authentication failures (in memory, newest first)"`
	Offenders []ban.Offender   `json:"offenders"`
	Allowlist []ban.AllowEntry `json:"allowlist"`
}

func banErr(d *Deps, op string, err error) error {
	switch {
	case errors.Is(err, ban.ErrNeverBan):
		return huma.Error422UnprocessableEntity(err.Error())
	case errors.Is(err, ban.ErrNotFound):
		return huma.Error404NotFound("no active ban for that network")
	case err != nil && strings.HasPrefix(err.Error(), "ban:"):
		return huma.Error422UnprocessableEntity(err.Error())
	default:
		return d.internalErr(op, err)
	}
}

func registerSecurity(api huma.API, d *Deps) {
	tags := []string{"security"}
	huma.Register(api, huma.Operation{OperationID: "get-security", Method: http.MethodGet, Path: "/security", Summary: "Bans, failures, allowlist, edge", Tags: tags},
		func(ctx context.Context, _ *struct{}) (*struct{ Body SecurityOverview }, error) {
			o := SecurityOverview{Edge: d.Edge}
			if d.Edge.Refresh != nil {
				d.Edge.Refresh(&o.Edge)
			}
			if o.Edge.NeverBan == nil {
				o.Edge.NeverBan = []string{}
			}
			var err error
			if o.Active, err = d.Bans.Active(ctx); err != nil {
				return nil, d.internalErr("security", err)
			}
			if o.History, err = d.Bans.History(ctx, 200); err != nil {
				return nil, d.internalErr("security", err)
			}
			if o.Offenders, err = d.Bans.Offenders(ctx); err != nil {
				return nil, d.internalErr("security", err)
			}
			if o.Allowlist, err = d.Bans.Allowlist(ctx); err != nil {
				return nil, d.internalErr("security", err)
			}
			o.Failures = d.Bans.Recent()
			return &struct{ Body SecurityOverview }{o}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "create-ban", Method: http.MethodPost, Path: "/security/bans", Summary: "Ban a network",
		Tags: tags, DefaultStatus: http.StatusCreated},
		func(ctx context.Context, in *struct {
			Body struct {
				Network       string `json:"network" maxLength:"64" doc:"IP or CIDR; a single IPv6 address bans its /64"`
				DurationHours int    `json:"duration_hours" minimum:"0" maximum:"8760" doc:"0 = no expiry"`
				Reason        string `json:"reason" maxLength:"256"`
			}
		}) (*struct{ Body ban.Ban }, error) {
			b, err := d.Bans.Manual(ctx, in.Body.Network, time.Duration(in.Body.DurationHours)*time.Hour, in.Body.Reason, d.actor(ctx))
			if err != nil {
				return nil, banErr(d, "create-ban", err)
			}
			return &struct{ Body ban.Ban }{*b}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "lift-ban", Method: http.MethodPost, Path: "/security/bans/lift", Summary: "Lift a ban",
		Tags: tags, DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, in *struct {
			Body struct {
				Network string `json:"network" maxLength:"64"`
			}
		}) (*struct{}, error) {
			if err := d.Bans.Unban(ctx, in.Body.Network, d.actor(ctx)); err != nil {
				return nil, banErr(d, "lift-ban", err)
			}
			return &struct{}{}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "add-allow", Method: http.MethodPost, Path: "/security/allowlist", Summary: "Never ban a network",
		Description: "Also lifts active bans inside it.", Tags: tags, DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, in *struct {
			Body struct {
				Network string `json:"network" maxLength:"64"`
				Note    string `json:"note,omitempty" maxLength:"256"`
			}
		}) (*struct{}, error) {
			if err := d.Bans.Allow(ctx, in.Body.Network, in.Body.Note, d.actor(ctx)); err != nil {
				return nil, banErr(d, "add-allow", err)
			}
			return &struct{}{}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "remove-allow", Method: http.MethodPost, Path: "/security/allowlist/remove", Summary: "Remove an allowlist entry",
		Tags: tags, DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, in *struct {
			Body struct {
				Network string `json:"network" maxLength:"64"`
			}
		}) (*struct{}, error) {
			if err := d.Bans.Disallow(ctx, in.Body.Network, d.actor(ctx)); err != nil {
				return nil, banErr(d, "remove-allow", err)
			}
			return &struct{}{}, nil
		})
}
