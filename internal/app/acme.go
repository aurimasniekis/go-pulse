package app

import (
	"context"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/go-acme/lego/v4/challenge"
	"github.com/go-acme/lego/v4/challenge/dns01"

	"github.com/aurimasniekis/go-pulse/internal/acme"
	"github.com/aurimasniekis/go-pulse/internal/api"
	"github.com/aurimasniekis/go-pulse/internal/bootstrap"
	"github.com/aurimasniekis/go-pulse/internal/config"
)

// certSources lists the certificates shown on the System page.
func certSources(cfg *config.Config, caPEM string) []api.CertSource {
	l := cfg.Layout
	out := []api.CertSource{
		{Name: "Internal CA", Purpose: "Signs the NATS bootstrap leaf; pulsed trusts only this CA for its own connections", PEM: caPEM},
		{Name: "NATS internal leaf", Purpose: "Served on 4222 for SNI nats (pulsed) and until ACME succeeds", Path: l.ServerCert()},
		{Name: "NATS Let's Encrypt", Purpose: "Served on 4222 for " + cfg.RawHost + " (ACME, DNS-01)", Path: l.ACMECert()},
	}
	if cfg.OriginCertFile != "" {
		out = append(out, api.CertSource{Name: "Origin certificate", Purpose: "Public listener (Cloudflare Origin CA)", Path: cfg.OriginCertFile})
	}
	if cfg.AOPCAFile != "" {
		out = append(out, api.CertSource{Name: "AOP client CA", Purpose: "Authenticated Origin Pulls: Cloudflare's client certificates chain to it", Path: cfg.AOPCAFile})
	}
	return out
}

// setupACME starts the Let's Encrypt manager for the raw NATS hostname (PL-EDGE-006, ADR-0023).
func (a *App) setupACME(cfg *config.Config, deps *api.Deps) error {
	deps.CertSources = certSources(cfg, a.Keys.CACert)
	if !cfg.ACMEEnabled {
		return nil
	}
	key, err := acme.ParseAccountKey(a.Keys.ACMEAccountKey)
	if err != nil {
		return err
	}
	var (
		provider challenge.Provider
		dnsOpts  []dns01.ChallengeOption
	)
	switch cfg.ACMEDNSProvider {
	case "cloudflare":
		if provider, err = acme.Cloudflare(cfg.ACMECFToken); err != nil {
			return err
		}
	case "challtestsrv":
		provider = &acme.ChallTestSrv{URL: cfg.ACMEChallTestSrv, HTTP: &http.Client{Timeout: 10 * time.Second}}
		dnsOpts = []dns01.ChallengeOption{dns01.DisableAuthoritativeNssPropagationRequirement(), dns01.PropagationWait(0, true)}
	}
	if len(cfg.ACMEDNSResolvers) > 0 {
		dnsOpts = append(dnsOpts, dns01.AddRecursiveNameservers(dns01.ParseNameservers(cfg.ACMEDNSResolvers)))
	}
	var dirCA []byte
	if cfg.ACMECAFile != "" {
		if dirCA, err = os.ReadFile(cfg.ACMECAFile); err != nil {
			return fmt.Errorf("PULSE_ACME_CA_FILE: %w", err)
		}
	}
	natsAddr := cfg.ACMENATSAddr
	if natsAddr == "" {
		u, err := url.Parse(cfg.NATSURL)
		if err != nil {
			return fmt.Errorf("PULSE_NATS_URL: %w", err)
		}
		natsAddr = u.Host
	}
	m, err := acme.New(acme.Options{
		Directory: cfg.ACMEDirectory, Email: cfg.ACMEEmail, Domain: cfg.RawHost, AccountKey: key,
		Provider: provider, DNSOpts: dnsOpts, DirectoryCA: dirCA,
		CertFile: cfg.Layout.ACMECert(), KeyFile: cfg.Layout.ACMEKey(),
		Install: func() error { _, err := bootstrap.Init(initOptions(cfg)); return err },
		Reload:  a.Sys.Reload,
		Served: func(ctx context.Context, serverName string, roots *x509.CertPool) (string, error) {
			return acme.ServedSerial(ctx, natsAddr, serverName, roots)
		},
		Log: a.Log.With("component", "acme"),
	})
	if err != nil {
		return err
	}
	a.ACME = m
	deps.ACME = m.Status
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		m.Run(a.loopCtx)
	}()
	return nil
}
