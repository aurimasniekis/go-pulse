package app

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/aurimasniekis/go-pulse/internal/api"
	"github.com/aurimasniekis/go-pulse/internal/audit"
	"github.com/aurimasniekis/go-pulse/internal/auth"
	"github.com/aurimasniekis/go-pulse/internal/bootstrap"
	"github.com/aurimasniekis/go-pulse/internal/certs"
	"github.com/aurimasniekis/go-pulse/internal/clients"
	"github.com/aurimasniekis/go-pulse/internal/config"
	"github.com/aurimasniekis/go-pulse/internal/hooks"
	"github.com/aurimasniekis/go-pulse/internal/natsconn"
	"github.com/aurimasniekis/go-pulse/internal/outbound"
	"github.com/aurimasniekis/go-pulse/internal/query"
	"github.com/aurimasniekis/go-pulse/internal/rollup"
	"github.com/aurimasniekis/go-pulse/internal/store"
	"github.com/aurimasniekis/go-pulse/internal/streams"
	"github.com/aurimasniekis/go-pulse/internal/sysapi"
	"github.com/aurimasniekis/go-pulse/internal/tail"
	"github.com/aurimasniekis/go-pulse/internal/tokens"
)

// App is a running pulsed.
type App struct {
	Cfg      *config.Config
	Log      *slog.Logger
	DB       *store.DB
	Keys     *bootstrap.KeySet
	SysConn  *nats.Conn
	AppConn  *nats.Conn
	JS       jetstream.JetStream
	Sys      *sysapi.Client
	Clients  *clients.Service
	Query    *query.Engine
	Tail     *tail.Manager
	Rollup   *rollup.Engine
	Outbound *outbound.Service

	ready     atomic.Bool
	listeners []*http.Server
	wg        sync.WaitGroup
	loopCtx   context.Context
	stopLoops context.CancelFunc
}

// Init runs `pulsed init` with the configuration.
func Init(cfg *config.Config, log *slog.Logger) error {
	res, err := bootstrap.Init(bootstrap.Options{
		Layout:          cfg.Layout,
		MasterKey:       cfg.MasterKey,
		RawHost:         cfg.RawHost,
		ClientListen:    cfg.NATSClientListen,
		MonitorListen:   cfg.NATSMonitorListen,
		WebsocketListen: cfg.NATSWebsocketListen,
		MaxFileStore:    cfg.NATSMaxFileStore,
	})
	if err != nil {
		return err
	}
	if len(res.Changed) == 0 {
		log.Info("init: nothing to do")
	}
	for _, p := range res.Changed {
		log.Info("init: wrote", "path", p)
	}
	return nil
}

// Start opens state, connects to NATS, syncs accounts and starts listeners. It returns once ready or failed.
func Start(ctx context.Context, cfg *config.Config, log *slog.Logger) (*App, error) {
	a := &App{Cfg: cfg, Log: log}
	// Background loops outlive Start's ctx (which only bounds startup) and stop on Close.
	a.loopCtx, a.stopLoops = context.WithCancel(context.WithoutCancel(ctx))
	ok := false
	defer func() {
		if !ok {
			a.Close()
		}
	}()
	var err error
	a.DB, err = store.Open(ctx, store.Options{Path: cfg.Layout.DatabaseFile(), LockPath: cfg.Layout.LockFile()})
	if err != nil {
		return nil, err
	}
	a.Keys, err = bootstrap.LoadKeySet(cfg.Layout.KeysFile(), cfg.MasterKey)
	if err != nil {
		return nil, err
	}
	// Health listener first so the container healthcheck can report "starting" vs "not ready".
	if err := a.startMetrics(ctx); err != nil {
		return nil, err
	}
	tlsConf, err := certs.ClientTLS(cfg.NATSTLSName, []byte(a.Keys.CACert))
	if err != nil {
		return nil, err
	}
	sysJWT, sysSeed, err := a.Keys.SysUser()
	if err != nil {
		return nil, err
	}
	a.SysConn, err = natsconn.Connect(ctx, natsconn.Options{URL: cfg.NATSURL, TLS: tlsConf, JWT: sysJWT, Seed: sysSeed, Name: "pulsed-sys", Logger: log})
	if err != nil {
		return nil, fmt.Errorf("connect SYS: %w", err)
	}
	a.Sys = sysapi.New(a.SysConn)
	a.Clients = clients.New(a.DB, a.Keys, a.Sys, log)
	if pushed, err := a.Clients.Sync(ctx, false); err != nil {
		return nil, fmt.Errorf("sync APP account: %w", err)
	} else if pushed {
		log.Info("APP account pushed to the resolver")
	}
	appJWT, appSeed, err := a.Keys.AppUser()
	if err != nil {
		return nil, err
	}
	a.AppConn, err = natsconn.Connect(ctx, natsconn.Options{URL: cfg.NATSURL, TLS: tlsConf, JWT: appJWT, Seed: appSeed, Name: "pulsed-app", Logger: log})
	if err != nil {
		return nil, fmt.Errorf("connect APP: %w", err)
	}
	a.JS, err = jetstream.New(a.AppConn)
	if err != nil {
		return nil, err
	}
	ens, err := streams.EnsureEvents(ctx, a.JS, cfg.EventsMaxBytes, false, log)
	if err != nil {
		return nil, err
	}
	for _, w := range ens.Warnings {
		log.Warn("EVENTS stream", "warning", w)
	}
	if err := streams.EnsureBlobs(ctx, a.JS); err != nil {
		return nil, fmt.Errorf("ensure blobs object store: %w", err)
	}
	a.Query = query.New(a.JS, 4, query.DefaultBudget)
	a.Tail = tail.New(a.JS, tail.DefaultMaxSessions, tail.DefaultRate)
	a.Rollup = rollup.New(a.JS, a.DB, rollup.Options{Log: log})
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		a.Rollup.Run(a.loopCtx) //nolint:contextcheck // runs until Close; flushes on exit
	}()
	a.Outbound, err = outbound.New(a.DB, a.JS, cfg.MasterKey, outbound.Options{Allow: cfg.OutboundAllow, Version: Version, Log: log})
	if err != nil {
		return nil, err
	}
	if err := a.Outbound.Start(a.loopCtx); err != nil { //nolint:contextcheck // dispatchers outlive startup
		return nil, fmt.Errorf("start outbound webhooks: %w", err)
	}
	hookSvc := hooks.New(a.DB, a.JS, cfg.MasterKey, log)
	authn, err := auth.New(cfg.AuthMode)
	if err != nil {
		return nil, err
	}
	deps := &api.Deps{Log: log, Version: Version, Profile: cfg.Profile, DB: a.DB, NC: a.AppConn, JS: a.JS, Sys: a.Sys,
		Query: a.Query, Tail: a.Tail, Rollup: a.Rollup, Auth: authn, Tokens: tokens.New(a.DB), Clients: a.Clients,
		Hooks: hookSvc, Outbound: a.Outbound,
		PublicNATSURL: cfg.PublicNATSURL, PublicURL: cfg.PublicURL, CACertPEM: a.Keys.CACert, AdminHosts: cfg.AdminHosts, Ready: a.Ready}
	if err := a.serve(ctx, "admin", cfg.AdminAddr, api.NewAdmin(deps)); err != nil {
		return nil, err
	}
	if cfg.PublicAddr != "" {
		var tlsConf *tls.Config
		if cfg.OriginCertFile != "" {
			cert, err := tls.LoadX509KeyPair(cfg.OriginCertFile, cfg.OriginKeyFile)
			if err != nil {
				return nil, fmt.Errorf("origin certificate: %w", err)
			}
			tlsConf = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
		}
		if err := a.serveTLS(ctx, "public", cfg.PublicAddr, api.NewPublic(deps), tlsConf); err != nil { //nolint:contextcheck // handlers use the per-request context from Huma
			return nil, err
		}
	}
	a.goLoop(a.loopCtx, "audit-prune", time.Hour, func(ctx context.Context) { //nolint:contextcheck // loops outlive startup
		if err := audit.Prune(ctx, a.DB, 400*24*time.Hour); err != nil {
			log.Warn("prune audit", "err", err)
		}
	})
	a.goLoop(a.loopCtx, "account-sync", time.Minute, func(ctx context.Context) { //nolint:contextcheck // loops outlive startup
		if _, err := a.Clients.Sync(ctx, false); err != nil {
			log.Warn("account sync", "err", err)
		}
		if n, err := a.Clients.PruneRevocations(ctx); err == nil && n > 0 {
			log.Info("pruned expired revocations", "count", n)
		}
	})
	a.ready.Store(true)
	ok = true
	log.Info("pulsed ready", "version", Version)
	return a, nil
}

// goLoop runs fn every interval until ctx ends. ctx is the app's loop context, deliberately not the startup ctx.
func (a *App) goLoop(ctx context.Context, name string, every time.Duration, fn func(context.Context)) {
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				func() {
					defer func() {
						if r := recover(); r != nil {
							a.Log.Error("loop panic", "loop", name, "panic", r)
						}
					}()
					fn(ctx)
				}()
			}
		}
	}()
}

func (a *App) startMetrics(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !a.Ready() {
			http.Error(w, "not ready\n", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ready\n"))
	})
	return a.serve(ctx, "metrics", a.Cfg.MetricsAddr, mux)
}

func (a *App) serve(ctx context.Context, name, addr string, h http.Handler) error {
	return a.serveTLS(ctx, name, addr, h, nil)
}

// serveTLS starts a listener; with tlsConf it serves HTTPS. WriteTimeout stays 0 because SSE streams are long-lived;
// Huma's SSE sets a per-write deadline instead.
func (a *App) serveTLS(ctx context.Context, name, addr string, h http.Handler, tlsConf *tls.Config) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("%s listener %s: %w", name, addr, err)
	}
	if tlsConf != nil {
		ln = tls.NewListener(ln, tlsConf)
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 64 << 10}
	a.listeners = append(a.listeners, srv)
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		a.Log.Info("listening", "listener", name, "addr", ln.Addr().String())
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			a.Log.Error("listener failed", "listener", name, "err", err)
		}
	}()
	return nil
}

// Ready reports readiness (PL-OBS-003).
func (a *App) Ready() bool {
	return a.ready.Load() && a.SysConn != nil && a.SysConn.IsConnected() && a.AppConn != nil && a.AppConn.IsConnected()
}

// Shutdown drains listeners and connections within the configured timeout (PL-OPS-010).
func (a *App) Shutdown(ctx context.Context) {
	a.ready.Store(false)
	ctx, cancel := context.WithTimeout(ctx, a.Cfg.ShutdownTimeout)
	defer cancel()
	for _, s := range a.listeners {
		_ = s.Shutdown(ctx)
	}
	a.Close()
}

// Close releases everything without draining.
func (a *App) Close() {
	if a.stopLoops != nil {
		a.stopLoops()
	}
	for _, s := range a.listeners {
		_ = s.Close()
	}
	for _, nc := range []*nats.Conn{a.AppConn, a.SysConn} {
		if nc != nil {
			_ = nc.Drain()
		}
	}
	a.wg.Wait()
	if a.Outbound != nil {
		a.Outbound.Wait()
	}
	if a.DB != nil {
		_ = a.DB.Close()
		a.DB = nil
	}
}
