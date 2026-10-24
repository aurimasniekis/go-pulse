package app

import (
	"log/slog"

	"github.com/aurimasniekis/go-pulse/internal/config"
	"github.com/aurimasniekis/go-pulse/internal/metrics"
	"github.com/aurimasniekis/go-pulse/internal/oidc"
	"github.com/aurimasniekis/go-pulse/internal/store"
	"github.com/aurimasniekis/go-pulse/internal/users"
)

// newUserAuth builds dashboard sign-in (ADR-0038). `pulsed admin` uses it too, so the CLI and the server agree on
// keys and policy.
func newUserAuth(db *store.DB, cfg *config.Config, log *slog.Logger) (*users.Service, *oidc.Service, error) {
	keys, err := users.DeriveKeys(cfg.MasterKey)
	if err != nil {
		return nil, nil, err
	}
	u := users.New(db, keys, users.Options{SessionIdle: cfg.SessionIdle, SessionAbsolute: cfg.SessionMax, ReauthWindow: cfg.ReauthWindow,
		MFARequiredRoles: cfg.MFARequiredRoles, OnLogin: func(method, result string) { metrics.Logins.WithLabelValues(method, result).Inc() }})
	o := oidc.New(u, oidc.Options{BaseURL: cfg.AdminURL, DevMode: cfg.Dev(), Logger: log})
	return u, o, nil
}

// NewUserAuth is newUserAuth for `pulsed admin`.
func NewUserAuth(db *store.DB, cfg *config.Config, log *slog.Logger) (*users.Service, error) {
	u, _, err := newUserAuth(db, cfg, log)
	return u, err
}
