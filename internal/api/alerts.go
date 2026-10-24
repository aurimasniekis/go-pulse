package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/aurimasniekis/go-pulse/internal/alerts"
	"github.com/aurimasniekis/go-pulse/internal/audit"
	"github.com/aurimasniekis/go-pulse/internal/notify"
)

// AlertsOverview is the Alerts page payload.
type AlertsOverview struct {
	Rules     []*alerts.AlertRule     `json:"rules"`
	Notifiers []notify.NotifierStatus `json:"notifiers" doc:"Configured notifiers (Telegram and Discord come from configuration files)"`
	Firing    int                     `json:"firing" doc:"Firing instances across all rules"`
}

type alertRuleIDInput struct {
	ID string `path:"id"`
}

func alertErr(d *Deps, op string, err error) error {
	switch {
	case errors.Is(err, alerts.ErrNotFound):
		return huma.Error404NotFound(err.Error())
	case errors.Is(err, alerts.ErrBuiltin):
		return huma.Error409Conflict(err.Error())
	case errors.Is(err, alerts.ErrInvalid):
		return huma.Error422UnprocessableEntity(err.Error())
	default:
		return d.internalErr(op, err)
	}
}

var errNoAlerts = huma.Error503ServiceUnavailable("the alert engine is not running")

func (d *Deps) alertsOverview(ctx context.Context) (*AlertsOverview, error) {
	rules, err := d.Alerts.List(ctx)
	if err != nil {
		return nil, err
	}
	out := &AlertsOverview{Rules: rules, Notifiers: []notify.NotifierStatus{}}
	for _, r := range rules {
		for _, in := range r.Instances {
			if in.State == "firing" {
				out.Firing++
			}
		}
	}
	for _, n := range d.Alerts.Notifiers() {
		out.Notifiers = append(out.Notifiers, n.Status())
	}
	return out, nil
}

func registerAlerts(api huma.API, d *Deps) {
	tags := []string{"alerts"}
	api.UseMiddleware(func(ctx huma.Context, next func(huma.Context)) {
		if d.Alerts == nil && len(ctx.Operation().Tags) > 0 && ctx.Operation().Tags[0] == "alerts" {
			_ = huma.WriteErr(api, ctx, errNoAlerts.GetStatus(), errNoAlerts.Error())
			return
		}
		next(ctx)
	})
	huma.Register(api, huma.Operation{OperationID: "get-alerts", Method: http.MethodGet, Path: "/alerts", Summary: "Alert rules, live state and notifiers", Tags: tags},
		func(ctx context.Context, _ *struct{}) (*struct{ Body *AlertsOverview }, error) {
			o, err := d.alertsOverview(ctx)
			if err != nil {
				return nil, alertErr(d, "get-alerts", err)
			}
			return &struct{ Body *AlertsOverview }{o}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "create-alert-rule", Method: http.MethodPost, Path: "/alerts/rules", Summary: "Create a custom threshold rule",
		Tags: tags, DefaultStatus: http.StatusCreated},
		func(ctx context.Context, in *struct{ Body alerts.RuleInput }) (*struct{ Body *alerts.AlertRule }, error) {
			r, err := d.Alerts.Create(ctx, in.Body, d.actor(ctx))
			if err != nil {
				return nil, alertErr(d, "create-alert-rule", err)
			}
			return &struct{ Body *alerts.AlertRule }{r}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "update-alert-rule", Method: http.MethodPut, Path: "/alerts/rules/{id}", Summary: "Update a rule", Tags: tags},
		func(ctx context.Context, in *struct {
			ID   string `path:"id"`
			Body alerts.RuleInput
		}) (*struct{ Body *alerts.AlertRule }, error) {
			r, err := d.Alerts.Update(ctx, in.ID, in.Body, d.actor(ctx))
			if err != nil {
				return nil, alertErr(d, "update-alert-rule", err)
			}
			return &struct{ Body *alerts.AlertRule }{r}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "delete-alert-rule", Method: http.MethodDelete, Path: "/alerts/rules/{id}", Summary: "Delete a custom rule",
		Tags: tags, DefaultStatus: http.StatusNoContent},
		func(ctx context.Context, in *alertRuleIDInput) (*struct{}, error) {
			if err := d.Alerts.Delete(ctx, in.ID, d.actor(ctx)); err != nil {
				return nil, alertErr(d, "delete-alert-rule", err)
			}
			return &struct{}{}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "evaluate-alerts", Method: http.MethodPost, Path: "/alerts/evaluate", Summary: "Evaluate all rules now", Tags: tags},
		func(ctx context.Context, _ *struct{}) (*struct{ Body *AlertsOverview }, error) {
			d.Alerts.Evaluate(context.WithoutCancel(ctx))
			o, err := d.alertsOverview(ctx)
			if err != nil {
				return nil, alertErr(d, "evaluate-alerts", err)
			}
			return &struct{ Body *AlertsOverview }{o}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "list-alert-history", Method: http.MethodGet, Path: "/alerts/history", Summary: "Firing and resolved transitions, newest first", Tags: tags},
		func(ctx context.Context, in *struct {
			Rule     string `query:"rule"`
			BeforeID int64  `query:"before_id" minimum:"0"`
			Limit    int    `query:"limit" minimum:"1" maximum:"500" default:"100"`
		}) (*struct{ Body []alerts.AlertHistoryEntry }, error) {
			h, err := d.Alerts.History(ctx, alerts.HistoryQuery{RuleID: in.Rule, BeforeID: in.BeforeID, Limit: in.Limit})
			if err != nil {
				return nil, alertErr(d, "list-alert-history", err)
			}
			return &struct{ Body []alerts.AlertHistoryEntry }{h}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "test-notifier", Method: http.MethodPost, Path: "/alerts/notifiers/{name}/test", Summary: "Send a test notification",
		Tags: tags},
		func(ctx context.Context, in *struct {
			Name string `path:"name"`
		}) (*struct{ Body notify.NotifierStatus }, error) {
			for _, n := range d.Alerts.Notifiers() {
				if n.Name() != in.Name {
					continue
				}
				err := n.Test(ctx)
				_ = audit.Write(ctx, d.DB, d.actor(ctx), "notifier.test", in.Name, auditResult(err), nil)
				if err != nil {
					return nil, huma.Error502BadGateway("test notification failed: " + err.Error())
				}
				return &struct{ Body notify.NotifierStatus }{n.Status()}, nil
			}
			return nil, huma.Error404NotFound("no such notifier")
		})
}

func auditResult(err error) string {
	if err != nil {
		return audit.Error
	}
	return audit.OK
}
