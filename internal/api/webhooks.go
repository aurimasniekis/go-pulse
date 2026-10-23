package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/aurimasniekis/go-pulse/internal/hooks"
	"github.com/aurimasniekis/go-pulse/internal/outbound"
)

// InboundInput creates or updates an inbound hook.
type InboundInput struct {
	Name         string       `json:"name" pattern:"^[a-z0-9][a-z0-9_-]{0,47}$"`
	Kind         string       `json:"kind" enum:"github,stripe,hmac,secret"`
	Subject      string       `json:"subject,omitempty" maxLength:"256" doc:"Default evt.hook.<name>.received"`
	Config       hooks.Config `json:"config,omitempty"`
	Secret       string       `json:"secret,omitempty" maxLength:"256" doc:"Provider secret; empty generates one (create) or keeps the current one (update)"`
	RotateSecret bool         `json:"rotate_secret,omitempty" doc:"Update only: generate a new secret"`
	Enabled      bool         `json:"enabled"`
}

// InboundHook is an inbound hook with its public URL.
type InboundHook struct {
	hooks.Hook
	URL string `json:"url" doc:"Where the provider sends deliveries"`
}

// OutboundInput creates or updates an outbound hook.
type OutboundInput struct {
	Name         string            `json:"name" pattern:"^[a-z0-9][a-z0-9_-]{0,47}$"`
	Filter       string            `json:"filter" maxLength:"256" doc:"Subject filter under evt.>"`
	URL          string            `json:"url" maxLength:"2048"`
	Headers      map[string]string `json:"headers,omitempty"`
	Secret       string            `json:"secret,omitempty" maxLength:"256" doc:"Signing secret; empty generates one (create) or keeps the current one (update)"`
	RotateSecret bool              `json:"rotate_secret,omitempty"`
}

func hookErr(d *Deps, op string, err error) error {
	switch {
	case errors.Is(err, hooks.ErrNotFound), errors.Is(err, outbound.ErrNotFound):
		return huma.Error404NotFound("hook not found")
	case errors.Is(err, hooks.ErrInvalid), errors.Is(err, outbound.ErrInvalid):
		return huma.Error422UnprocessableEntity(err.Error())
	default:
		return d.internalErr(op, err)
	}
}

func (d *Deps) inbound(h *hooks.Hook) InboundHook {
	base := d.PublicURL
	if base == "" {
		base = "https://<public host>"
	}
	return InboundHook{Hook: *h, URL: base + "/hooks/" + h.Name}
}

type idPath struct {
	ID string `path:"id" pattern:"^[a-z0-9]{6,32}$"`
}

type secretOut[T any] struct {
	Body struct {
		Hook   T      `json:"hook"`
		Secret string `json:"secret,omitempty" doc:"Shown once, when created or rotated"`
	}
}

func registerWebhooks(api huma.API, d *Deps) {
	in, out := []string{"webhooks-in"}, []string{"webhooks-out"}
	huma.Register(api, huma.Operation{OperationID: "list-inbound-hooks", Method: http.MethodGet, Path: "/hooks/inbound", Summary: "List inbound hooks", Tags: in},
		func(ctx context.Context, _ *struct{}) (*struct{ Body []InboundHook }, error) {
			hs, err := d.Hooks.List(ctx)
			if err != nil {
				return nil, d.internalErr("list-inbound", err)
			}
			o := &struct{ Body []InboundHook }{Body: make([]InboundHook, 0, len(hs))}
			for _, h := range hs {
				o.Body = append(o.Body, d.inbound(h))
			}
			return o, nil
		})
	huma.Register(api, huma.Operation{OperationID: "create-inbound-hook", Method: http.MethodPost, Path: "/hooks/inbound", Summary: "Create an inbound hook",
		Tags: in, DefaultStatus: http.StatusCreated},
		func(ctx context.Context, i *struct{ Body InboundInput }) (*secretOut[InboundHook], error) {
			b := i.Body
			h, secret, err := d.Hooks.Create(ctx, hooks.Input{Name: b.Name, Kind: b.Kind, Subject: b.Subject, Config: b.Config, Secret: b.Secret,
				Enabled: b.Enabled}, d.actor(ctx))
			if err != nil {
				return nil, hookErr(d, "create-inbound", err)
			}
			o := &secretOut[InboundHook]{}
			o.Body.Hook, o.Body.Secret = d.inbound(h), secret
			return o, nil
		})
	huma.Register(api, huma.Operation{OperationID: "update-inbound-hook", Method: http.MethodPut, Path: "/hooks/inbound/{id}", Summary: "Update an inbound hook", Tags: in},
		func(ctx context.Context, i *struct {
			ID   string `path:"id" pattern:"^[a-z0-9]{6,32}$"`
			Body InboundInput
		}) (*secretOut[InboundHook], error) {
			b := i.Body
			secret := b.Secret
			if b.RotateSecret && secret == "" {
				secret = hooks.GenerateSecret()
			}
			h, err := d.Hooks.Update(ctx, i.ID, hooks.Input{Name: b.Name, Kind: b.Kind, Subject: b.Subject, Config: b.Config, Secret: secret,
				Enabled: b.Enabled}, d.actor(ctx))
			if err != nil {
				return nil, hookErr(d, "update-inbound", err)
			}
			o := &secretOut[InboundHook]{}
			o.Body.Hook = d.inbound(h)
			if b.RotateSecret {
				o.Body.Secret = secret
			}
			return o, nil
		})
	huma.Register(api, huma.Operation{OperationID: "delete-inbound-hook", Method: http.MethodDelete, Path: "/hooks/inbound/{id}", Summary: "Delete an inbound hook",
		Tags: in, DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, i *idPath) (*struct{}, error) {
			if err := d.Hooks.Delete(ctx, i.ID, d.actor(ctx)); err != nil {
				return nil, hookErr(d, "delete-inbound", err)
			}
			return &struct{}{}, nil
		})

	huma.Register(api, huma.Operation{OperationID: "list-outbound-hooks", Method: http.MethodGet, Path: "/hooks/outbound", Summary: "List outbound hooks", Tags: out},
		func(ctx context.Context, _ *struct{}) (*struct{ Body []*outbound.Hook }, error) {
			hs, err := d.Outbound.List(ctx)
			if err != nil {
				return nil, d.internalErr("list-outbound", err)
			}
			return &struct{ Body []*outbound.Hook }{hs}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "create-outbound-hook", Method: http.MethodPost, Path: "/hooks/outbound", Summary: "Create an outbound hook",
		Description: "Delivers events published after creation.", Tags: out, DefaultStatus: http.StatusCreated},
		func(ctx context.Context, i *struct{ Body OutboundInput }) (*secretOut[*outbound.Hook], error) {
			b := i.Body
			h, secret, err := d.Outbound.Create(ctx, outbound.Input{Name: b.Name, Filter: b.Filter, URL: b.URL, Headers: b.Headers, Secret: b.Secret}, d.actor(ctx))
			if err != nil {
				return nil, hookErr(d, "create-outbound", err)
			}
			o := &secretOut[*outbound.Hook]{}
			o.Body.Hook, o.Body.Secret = h, secret
			return o, nil
		})
	huma.Register(api, huma.Operation{OperationID: "get-outbound-hook", Method: http.MethodGet, Path: "/hooks/outbound/{id}", Summary: "Get an outbound hook", Tags: out},
		func(ctx context.Context, i *idPath) (*struct{ Body *outbound.Hook }, error) {
			h, err := d.Outbound.Get(ctx, i.ID)
			if err != nil {
				return nil, hookErr(d, "get-outbound", err)
			}
			return &struct{ Body *outbound.Hook }{h}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "update-outbound-hook", Method: http.MethodPut, Path: "/hooks/outbound/{id}", Summary: "Update an outbound hook", Tags: out},
		func(ctx context.Context, i *struct {
			ID   string `path:"id" pattern:"^[a-z0-9]{6,32}$"`
			Body OutboundInput
		}) (*secretOut[*outbound.Hook], error) {
			b := i.Body
			secret := b.Secret
			if b.RotateSecret && secret == "" {
				secret = "whsec_" + hooks.GenerateSecret()
			}
			h, err := d.Outbound.Update(ctx, i.ID, outbound.Input{Name: b.Name, Filter: b.Filter, URL: b.URL, Headers: b.Headers, Secret: secret}, d.actor(ctx))
			if err != nil {
				return nil, hookErr(d, "update-outbound", err)
			}
			o := &secretOut[*outbound.Hook]{}
			o.Body.Hook = h
			if b.RotateSecret {
				o.Body.Secret = secret
			}
			return o, nil
		})
	huma.Register(api, huma.Operation{OperationID: "delete-outbound-hook", Method: http.MethodDelete, Path: "/hooks/outbound/{id}", Summary: "Delete an outbound hook",
		Tags: out, DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, i *idPath) (*struct{}, error) {
			if err := d.Outbound.Delete(ctx, i.ID, d.actor(ctx)); err != nil {
				return nil, hookErr(d, "delete-outbound", err)
			}
			return &struct{}{}, nil
		})
	for _, paused := range []bool{true, false} {
		op, path, sum := "pause-outbound-hook", "/hooks/outbound/{id}/pause", "Pause deliveries (keeps position)"
		if !paused {
			op, path, sum = "resume-outbound-hook", "/hooks/outbound/{id}/resume", "Resume deliveries"
		}
		huma.Register(api, huma.Operation{OperationID: op, Method: http.MethodPost, Path: path, Summary: sum, Tags: out},
			func(ctx context.Context, i *idPath) (*struct{ Body *outbound.Hook }, error) {
				h, err := d.Outbound.SetPaused(ctx, i.ID, paused, d.actor(ctx))
				if err != nil {
					return nil, hookErr(d, op, err)
				}
				return &struct{ Body *outbound.Hook }{h}, nil
			})
	}
	huma.Register(api, huma.Operation{OperationID: "test-outbound-hook", Method: http.MethodPost, Path: "/hooks/outbound/{id}/test", Summary: "Send a test event", Tags: out},
		func(ctx context.Context, i *idPath) (*struct{ Body *outbound.Delivery }, error) {
			dl, err := d.Outbound.Test(ctx, i.ID, d.actor(ctx))
			if err != nil {
				return nil, hookErr(d, "test-outbound", err)
			}
			return &struct{ Body *outbound.Delivery }{dl}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "list-deliveries", Method: http.MethodGet, Path: "/hooks/outbound/{id}/deliveries", Summary: "Delivery log", Tags: out},
		func(ctx context.Context, i *struct {
			ID       string `path:"id" pattern:"^[a-z0-9]{6,32}$"`
			BeforeID int64  `query:"before_id" minimum:"0"`
			Limit    int    `query:"limit" default:"100" minimum:"1" maximum:"500"`
		}) (*struct{ Body []outbound.Delivery }, error) {
			ds, err := d.Outbound.Deliveries(ctx, i.ID, i.BeforeID, i.Limit)
			if err != nil {
				return nil, d.internalErr("deliveries", err)
			}
			return &struct{ Body []outbound.Delivery }{ds}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "retry-delivery", Method: http.MethodPost, Path: "/hooks/deliveries/{delivery}/retry", Summary: "Deliver a logged event again",
		Tags: out},
		func(ctx context.Context, i *struct {
			Delivery int64 `path:"delivery" minimum:"1"`
		}) (*struct{ Body *outbound.Delivery }, error) {
			dl, err := d.Outbound.Retry(ctx, i.Delivery, d.actor(ctx))
			if err != nil {
				return nil, hookErr(d, "retry-delivery", err)
			}
			return &struct{ Body *outbound.Delivery }{dl}, nil
		})
}
