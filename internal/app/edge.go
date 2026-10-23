package app

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/aurimasniekis/go-pulse/internal/api"
	"github.com/aurimasniekis/go-pulse/internal/ban"
	"github.com/aurimasniekis/go-pulse/internal/config"
	"github.com/aurimasniekis/go-pulse/internal/edge"
	"github.com/aurimasniekis/go-pulse/internal/hooks"
	"github.com/aurimasniekis/go-pulse/internal/sysapi"
)

// edgeParts are the public-listener protections built from configuration (M8).
type edgeParts struct {
	ranges *edge.Ranges
	realIP *edge.RealIP
	certs  *edge.CertReloader
	tls    *tls.Config
	ws     *url.URL
	info   api.EdgeInfo
}

// setupEdge builds the Cloudflare ranges, real-IP rule, ban service and detectors, TLS with AOP, and the /ws target.
func (a *App) setupEdge(ctx context.Context, cfg *config.Config, deps *api.Deps, hookSvc *hooks.Service) (*edgeParts, error) {
	log := a.Log
	p := &edgeParts{}
	var err error
	if p.ranges, err = edge.NewRanges(edge.DefaultCloudflare); err != nil {
		return nil, err
	}
	p.realIP = &edge.RealIP{Cloudflare: p.ranges, RequireAOP: cfg.AOPCAFile != "" || cfg.Profile == config.ProfileProd}
	if cfg.OriginCertFile != "" {
		if p.certs, err = edge.NewCertReloader(cfg.OriginCertFile, cfg.OriginKeyFile, log); err != nil {
			return nil, err
		}
		if p.tls, err = edge.ServerTLS(p.certs, cfg.AOPCAFile); err != nil {
			return nil, err
		}
	}
	if cfg.NATSWebsocketListen != "" && cfg.NATSWebsocketURL != "" {
		if p.ws, err = url.Parse(cfg.NATSWebsocketURL); err != nil {
			return nil, fmt.Errorf("PULSE_NATS_WEBSOCKET_URL: %w", err)
		}
	}

	neverBan := append(append([]string{}, cfg.BanAllow...), cfg.HostIPs...)
	a.Bans, err = ban.New(a.DB, ban.Options{Threshold: cfg.BanThreshold, Window: cfg.BanWindow, Base: cfg.BanDuration, Max: cfg.BanMax,
		NeverBan: neverBan, IsCloudflare: p.ranges.Contains, Log: log})
	if err != nil {
		return nil, err
	}
	if err := a.Bans.Load(ctx); err != nil {
		return nil, err
	}
	// Detectors (PL-BAN-001): NATS auth errors, HTTP token failures, bad webhook signatures.
	onAuthErr := func(adv sysapi.Advisory) { a.Bans.Fail(a.loopCtx, adv.Client.Host, "nats", adv.Reason) }
	if _, err := a.Sys.Subscribe(sysapi.SubjAuthErr, onAuthErr); err != nil {
		return nil, fmt.Errorf("subscribe to auth errors: %w", err)
	}
	fail := func(ip, source string) { a.Bans.Fail(a.loopCtx, ip, source, "") }
	deps.OnAuthFailure = fail
	hookSvc.OnFailure = fail
	hookSvc.ClientIP = p.realIP.ClientIP
	deps.Bans, deps.ClientIP, deps.WSTarget = a.Bans, p.realIP.ClientIP, p.ws

	// banagent feed on the metrics listener (host loopback only).
	if tok, err := os.ReadFile(cfg.Layout.BanagentTokenFile()); err == nil && len(strings.TrimSpace(string(tok))) > 0 {
		a.metrics.Handle("GET /internal/v1/bans", ban.NewFeed(a.Bans, string(tok)))
		p.info.BanAgent = true
	} else {
		log.Warn("banagent token missing; the ban feed is disabled (run pulsed init)", "file", cfg.Layout.BanagentTokenFile())
	}

	a.goLoop(a.loopCtx, "ban-prune", time.Hour, func(ctx context.Context) { //nolint:contextcheck // loops outlive startup
		if err := a.Bans.Prune(ctx); err != nil {
			log.Warn("prune bans", "err", err)
		}
	})
	if cfg.CFRefresh {
		refresh := func(ctx context.Context) {
			c := &http.Client{Timeout: 15 * time.Second}
			if cidrs, err := edge.FetchCloudflare(ctx, c, edge.CloudflareIPsURL); err != nil {
				log.Warn("refresh Cloudflare ranges; keeping the current list", "err", err)
			} else if err := p.ranges.Set(cidrs); err == nil {
				log.Info("Cloudflare ranges refreshed", "count", len(cidrs))
			}
		}
		a.wg.Add(1)
		go func() { // first refresh soon after start, then daily
			defer a.wg.Done()
			select {
			case <-a.loopCtx.Done():
				return
			case <-time.After(30 * time.Second):
			}
			refresh(a.loopCtx) //nolint:contextcheck // loop context
		}()
		a.goLoop(a.loopCtx, "cf-ranges", 24*time.Hour, refresh) //nolint:contextcheck // loops outlive startup
	}
	if cfg.CFAccountID != "" {
		cl := &ban.CloudflareList{BaseURL: "https://api.cloudflare.com/client/v4", AccountID: cfg.CFAccountID, ListID: cfg.CFBanListID,
			Token: cfg.CFBanListToken, HTTP: &http.Client{Timeout: 20 * time.Second}, Log: log}
		a.wg.Add(1)
		go func() {
			defer a.wg.Done()
			cl.Run(a.loopCtx, a.Bans) //nolint:contextcheck // loop context
		}()
		p.info.CFListSync = true
	}

	p.info.TLS, p.info.AOP, p.info.NeverBan = p.tls != nil, cfg.AOPCAFile != "", neverBan
	p.info.Refresh = func(e *api.EdgeInfo) {
		e.CloudflareCount, e.CloudflareAt = len(p.ranges.List()), p.ranges.Updated()
		if p.certs != nil {
			if l := p.certs.Leaf(); l != nil {
				e.OriginNotAfter = l.NotAfter
			}
		}
	}
	deps.Edge = p.info
	return p, nil
}
