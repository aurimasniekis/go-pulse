// Package acme obtains and renews the Let's Encrypt certificate for raw NATS (PL-EDGE-006, ADR-0010) with DNS-01.
//
// The certificate is written next to the internal leaf (acme.crt/acme.key) and listed first in nats.conf, so NATS
// serves it to clients that use the public hostname while pulsed keeps verifying the internal leaf via SNI "nats".
package acme

import (
	"bytes"
	"context"
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge"
	"github.com/go-acme/lego/v4/challenge/dns01"
	"github.com/go-acme/lego/v4/lego"
	legolog "github.com/go-acme/lego/v4/log"
	"github.com/go-acme/lego/v4/providers/dns/cloudflare"
	"github.com/go-acme/lego/v4/registration"

	"github.com/aurimasniekis/go-pulse/internal/certs"
	"github.com/aurimasniekis/go-pulse/internal/secretbox"
)

// LetsEncrypt is the production directory.
const LetsEncrypt = "https://acme-v02.api.letsencrypt.org/directory"

// RenewBefore is when renewal starts for 90-day certificates (PL-EDGE-006); see renewWindow.
const RenewBefore = 30 * 24 * time.Hour

// Options configure the manager.
type Options struct {
	Directory   string
	Email       string
	Domain      string
	AccountKey  crypto.PrivateKey
	Provider    challenge.Provider
	DNSOpts     []dns01.ChallengeOption
	DirectoryCA []byte // extra roots to trust the ACME directory (pebble in dev/tests)
	CertFile    string // where the certificate (with chain) goes
	KeyFile     string
	// Install re-renders nats.conf so it lists the ACME pair (pulsed calls bootstrap.Init).
	Install func() error
	// Reload asks NATS to reload its configuration.
	Reload func(ctx context.Context) error
	// Served returns the serial of the certificate NATS presents for the domain (SNI) given trust roots.
	Served func(ctx context.Context, serverName string, roots *x509.CertPool) (string, error)
	Log    *slog.Logger
	Now    func() time.Time
}

// Status is shown on the System page.
type Status struct {
	Domain      string    `json:"domain"`
	Directory   string    `json:"directory"`
	Serial      string    `json:"serial,omitempty"`
	Issuer      string    `json:"issuer,omitempty"`
	NotAfter    time.Time `json:"not_after,omitzero"`
	LastAttempt time.Time `json:"last_attempt,omitzero"`
	LastSuccess time.Time `json:"last_success,omitzero"`
	LastError   string    `json:"last_error,omitempty"`
	Verified    bool      `json:"verified" doc:"NATS was seen serving this certificate after the last reload"`
}

// Manager keeps the certificate fresh.
type Manager struct {
	o  Options
	mu sync.Mutex
	st Status
}

// New validates options.
func New(o Options) (*Manager, error) {
	if o.Domain == "" || o.Provider == nil || o.AccountKey == nil || o.CertFile == "" || o.KeyFile == "" {
		return nil, errors.New("acme: domain, provider, account key and file paths are required")
	}
	if o.Directory == "" {
		o.Directory = LetsEncrypt
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	legolog.Logger = slog.NewLogLogger(o.Log.Handler(), slog.LevelInfo)
	if o.Now == nil {
		o.Now = time.Now
	}
	m := &Manager{o: o, st: Status{Domain: o.Domain, Directory: o.Directory}}
	if c, err := m.current(); err == nil {
		m.st.Serial, m.st.Issuer, m.st.NotAfter = certs.SerialHex(c), c.Issuer.CommonName, c.NotAfter
	}
	return m, nil
}

// Status returns the current status.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.st
}

func (m *Manager) current() (*x509.Certificate, error) {
	b, err := os.ReadFile(m.o.CertFile)
	if err != nil {
		return nil, err
	}
	return certs.ParseLeaf(b)
}

// Due reports whether a (re)issue is needed: no certificate, wrong name, or inside the renewal window.
func (m *Manager) Due() bool {
	c, err := m.current()
	if err != nil {
		return true
	}
	if c.VerifyHostname(m.o.Domain) != nil {
		return true
	}
	return c.NotAfter.Sub(m.o.Now()) < renewWindow(c)
}

// renewWindow is RenewBefore, or a third of the lifetime for short-lived certificates (Let's Encrypt's 45-day and
// 6-day profiles), so a short certificate is not renewed on every check.
func renewWindow(c *x509.Certificate) time.Duration {
	return min(RenewBefore, c.NotAfter.Sub(c.NotBefore)/3)
}

type user struct {
	email string
	key   crypto.PrivateKey
	reg   *registration.Resource
}

func (u *user) GetEmail() string                        { return u.email }
func (u *user) GetRegistration() *registration.Resource { return u.reg }
func (u *user) GetPrivateKey() crypto.PrivateKey        { return u.key }

func (m *Manager) client() (*lego.Client, error) {
	u := &user{email: m.o.Email, key: m.o.AccountKey}
	cfg := lego.NewConfig(u)
	cfg.CADirURL = m.o.Directory
	cfg.Certificate.KeyType = certcrypto.EC256 // gitleaks:allow (key type constant)
	cfg.UserAgent = "pulsed"
	if len(m.o.DirectoryCA) > 0 {
		hc, err := DirectoryClient(m.o.DirectoryCA)
		if err != nil {
			return nil, err
		}
		cfg.HTTPClient = hc
	}
	c, err := lego.NewClient(cfg)
	if err != nil {
		return nil, err
	}
	if err := c.Challenge.SetDNS01Provider(m.o.Provider, m.o.DNSOpts...); err != nil {
		return nil, err
	}
	reg, err := c.Registration.ResolveAccountByKey()
	if err != nil {
		if reg, err = c.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true}); err != nil {
			return nil, fmt.Errorf("acme: register account: %w", err)
		}
	}
	u.reg = reg
	return c, nil
}

// DirectoryClient trusts the system roots plus extra PEM roots for the ACME directory (pebble's minica in dev/tests).
func DirectoryClient(extraRoots []byte) (*http.Client, error) {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(extraRoots) {
		return nil, errors.New("acme: invalid directory CA PEM")
	}
	return &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, Proxy: http.ProxyFromEnvironment}}, nil
}

// Ensure obtains a certificate when due, installs it, reloads NATS and verifies the served serial.
func (m *Manager) Ensure(ctx context.Context) error {
	if !m.Due() {
		return nil
	}
	m.mu.Lock()
	m.st.LastAttempt = m.o.Now().UTC()
	m.mu.Unlock()
	err := m.issue(ctx)
	m.mu.Lock()
	if err != nil {
		m.st.LastError = err.Error()
	} else {
		m.st.LastError, m.st.LastSuccess = "", m.o.Now().UTC()
	}
	m.mu.Unlock()
	return err
}

func (m *Manager) issue(ctx context.Context) error {
	c, err := m.client()
	if err != nil {
		return err
	}
	res, err := c.Certificate.Obtain(certificate.ObtainRequest{Domains: []string{m.o.Domain}, Bundle: true})
	if err != nil {
		return fmt.Errorf("acme: obtain %s: %w", m.o.Domain, err)
	}
	leaf, err := certs.ParseLeaf(res.Certificate)
	if err != nil {
		return err
	}
	// Key first: NATS loads both on reload, and a new certificate with the old key would not load.
	if err := secretbox.WriteFileAtomic(m.o.KeyFile, res.PrivateKey, 0o600); err != nil {
		return err
	}
	if err := secretbox.WriteFileAtomic(m.o.CertFile, res.Certificate, 0o644); err != nil {
		return err
	}
	serial := certs.SerialHex(leaf)
	m.mu.Lock()
	m.st.Serial, m.st.Issuer, m.st.NotAfter, m.st.Verified = serial, leaf.Issuer.CommonName, leaf.NotAfter, false
	m.mu.Unlock()
	m.o.Log.Info("acme certificate obtained", "domain", m.o.Domain, "serial", serial, "not_after", leaf.NotAfter)
	if m.o.Install != nil {
		if err := m.o.Install(); err != nil {
			return fmt.Errorf("acme: install: %w", err)
		}
	}
	if m.o.Reload == nil || m.o.Served == nil {
		return nil
	}
	if err := m.o.Reload(ctx); err != nil {
		return fmt.Errorf("acme: reload NATS: %w", err)
	}
	roots := chainRoots(res.Certificate)
	var got string
	for i := range 10 {
		got, err = m.o.Served(ctx, m.o.Domain, roots)
		if err == nil && got == serial {
			m.mu.Lock()
			m.st.Verified = true
			m.mu.Unlock()
			m.o.Log.Info("NATS serves the new certificate", "serial", serial)
			return nil
		}
		time.Sleep(time.Duration(i+1) * 200 * time.Millisecond)
	}
	return fmt.Errorf("acme: NATS serves serial %q, want %q (last error: %w)", got, serial, err)
}

// chainRoots trusts the system roots plus the issuer certificates from the obtained bundle, so the check proves NATS
// serves exactly the certificate just obtained, for any CA (Let's Encrypt or pebble).
func chainRoots(bundle []byte) *x509.CertPool {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	rest := bundle
	first := true
	for {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			return pool
		}
		if first {
			first = false
			continue
		}
		if c, err := x509.ParseCertificate(b.Bytes); err == nil {
			pool.AddCert(c)
		}
	}
}

// Run ensures the certificate shortly after start and then twice a day; failures retry after an hour.
func (m *Manager) Run(ctx context.Context) {
	wait := 10 * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if err := m.Ensure(ctx); err != nil {
			m.o.Log.Error("acme", "err", err)
			wait = time.Hour
			continue
		}
		wait = 12 * time.Hour
	}
}

// Cloudflare returns the DNS-01 provider for Cloudflare with a scoped API token (Zone.DNS edit + Zone read).
func Cloudflare(token string) (challenge.Provider, error) {
	cfg := cloudflare.NewDefaultConfig()
	cfg.AuthToken = strings.TrimSpace(token)
	cfg.PropagationTimeout = 3 * time.Minute
	cfg.PollingInterval = 5 * time.Second
	return cloudflare.NewDNSProviderConfig(cfg)
}

// ChallTestSrv is a DNS-01 provider for pebble's challenge test server (dev and tests only).
type ChallTestSrv struct {
	URL  string // management API, e.g. http://challtestsrv:8055
	HTTP *http.Client
}

func (p *ChallTestSrv) post(path string, body any) error {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, strings.TrimRight(p.URL, "/")+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("challtestsrv %s: %s", path, resp.Status)
	}
	return nil
}

// Present publishes the TXT record.
func (p *ChallTestSrv) Present(domain, _, keyAuth string) error {
	info := dns01.GetChallengeInfo(domain, keyAuth)
	return p.post("/set-txt", map[string]string{"host": info.EffectiveFQDN, "value": info.Value})
}

// CleanUp removes the TXT record.
func (p *ChallTestSrv) CleanUp(domain, _, keyAuth string) error {
	info := dns01.GetChallengeInfo(domain, keyAuth)
	return p.post("/clear-txt", map[string]string{"host": info.EffectiveFQDN})
}

// ParseAccountKey reads the PEM EC key stored in the key set.
func ParseAccountKey(p string) (crypto.PrivateKey, error) {
	b, _ := pem.Decode([]byte(p))
	if b == nil {
		return nil, errors.New("acme: no account key")
	}
	return x509.ParseECPrivateKey(b.Bytes)
}
