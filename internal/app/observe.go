package app

import (
	"context"
	"net/http"
	"time"

	"github.com/aurimasniekis/go-pulse/internal/alerts"
	"github.com/aurimasniekis/go-pulse/internal/api"
	"github.com/aurimasniekis/go-pulse/internal/config"
	"github.com/aurimasniekis/go-pulse/internal/metrics"
	"github.com/aurimasniekis/go-pulse/internal/notify"
)

// notifiers builds the Telegram and Discord notifiers from configuration (PL-OBS-006).
func notifiers(cfg *config.Config, a *App) []*notify.Notifier {
	hc := &http.Client{Timeout: 15 * time.Second}
	var out []*notify.Notifier
	if cfg.TelegramToken != "" {
		out = append(out, notify.New("telegram", &notify.Telegram{APIURL: cfg.TelegramAPIURL, Token: cfg.TelegramToken, ChatID: cfg.TelegramChatID, HTTP: hc},
			notify.Options{MinSeverity: cfg.TelegramMinSeverity, PerMinute: 20, Burst: 10, Log: a.Log}))
	}
	if cfg.DiscordWebhookURL != "" {
		out = append(out, notify.New("discord", &notify.Discord{WebhookURL: cfg.DiscordWebhookURL, HTTP: hc},
			notify.Options{MinSeverity: cfg.DiscordMinSeverity, PerMinute: 25, Burst: 5, Log: a.Log}))
	}
	return out
}

// certExpiries reads every known certificate (System page sources) for alerts and metrics.
func certExpiries(sources []api.CertSource) []alerts.Cert {
	now := time.Now()
	var out []alerts.Cert
	for _, s := range sources {
		if ci := api.DescribeCert(s, now); ci.Present {
			out = append(out, alerts.Cert{Name: ci.Name, NotAfter: ci.NotAfter})
		}
	}
	return out
}

// setupObservability starts the alert engine and registers the scrape collector (PL-OBS-002/004).
func (a *App) setupObservability(ctx context.Context, cfg *config.Config, deps *api.Deps, backupStatus func(context.Context) (alerts.BackupStatus, bool)) error {
	certs := func() []alerts.Cert { return certExpiries(deps.CertSources) }
	a.Alerts = alerts.New(a.DB, alerts.Providers{
		JS: a.JS, Sys: a.Sys, Rollup: a.Rollup, Certs: certs, Backup: backupStatus,
		NATSConnected: func() bool { return a.SysConn != nil && a.SysConn.IsConnected() },
	}, alerts.Options{Interval: cfg.AlertInterval, Repeat: cfg.AlertRepeat, Publish: a.AppConn, Notifiers: notifiers(cfg, a), Log: a.Log.With("component", "alerts")})
	if err := a.Alerts.Seed(ctx); err != nil {
		return err
	}
	deps.Alerts = a.Alerts
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		a.Alerts.Run(a.loopCtx) //nolint:contextcheck // runs until Close
	}()
	a.goLoop(a.loopCtx, "alert-history-prune", 6*time.Hour, func(ctx context.Context) { //nolint:contextcheck // loops outlive startup
		if err := a.Alerts.Prune(ctx); err != nil {
			a.Log.Warn("prune alert history", "err", err)
		}
	})

	metrics.SetSources(metrics.Sources{
		Version: Version, Sys: a.Sys, JS: a.JS,
		NATSConnected: func() bool { return a.AppConn != nil && a.AppConn.IsConnected() },
		TailSessions:  a.Tail.Active,
		ActiveBans:    a.Bans.ActiveCount,
		AlertsFiring:  func() map[string]int { return a.Alerts.Firing(a.loopCtx) }, //nolint:contextcheck // scrape-time read
		Certs: func() []metrics.CertExpiry {
			var out []metrics.CertExpiry
			for _, c := range certs() {
				out = append(out, metrics.CertExpiry{Name: c.Name, NotAfter: c.NotAfter})
			}
			return out
		},
	})
	return nil
}
