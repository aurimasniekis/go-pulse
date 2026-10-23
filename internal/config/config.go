// Package config loads pulsed configuration from PULSE_* environment variables, with *_FILE variants for secrets
// (PL-OPS-003/004). docs/operator/configuration.md documents every variable and is the contract with deployments.
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aurimasniekis/go-pulse/internal/bootstrap"
	"github.com/aurimasniekis/go-pulse/internal/secretbox"
)

// Profiles.
const (
	ProfileDev  = "dev"
	ProfileProd = "prod"
)

// Config is the full pulsed configuration.
type Config struct {
	Profile  string
	LogLevel string
	Layout   bootstrap.Layout

	MasterKey []byte

	// NATS
	NATSURL             string // pulsed's own connection, e.g. tls://nats:4222
	NATSTLSName         string // server name verified on the NATS TLS cert (ADR-0023)
	NATSClientListen    string // rendered into nats.conf by init
	NATSMonitorListen   string
	NATSWebsocketListen string
	NATSWebsocketURL    string // where pulsed proxies /ws to
	NATSMaxFileStore    int64
	RawHost             string // r.events.wan.lt
	PublicNATSURL       string // what tools use, shown in snippets
	PublicURL           string // public HTTPS base URL, shown in snippets
	PublicHost          string // events.wan.lt
	EventsMaxBytes      int64

	// Listeners
	PublicAddr             string
	AdminAddr              string
	MetricsAddr            string
	AdminPublishedLoopback bool // compose publishes the admin port only on host loopback (ADR-0008)
	AdminPublic            bool
	AuthMode               string   // none | oidc
	AdminHosts             []string // extra Host names accepted by the admin listener (DNS-rebinding guard)
	OutboundAllow          []string // networks outbound webhooks may reach despite the SSRF guard
	AOPCAFile              string   // client CA for Authenticated Origin Pulls on the public listener
	BanThreshold           int
	BanWindow              time.Duration
	BanDuration            time.Duration
	BanMax                 time.Duration
	BanAllow               []string // never-ban networks (operator allowlist from config)
	HostIPs                []string // the host's own addresses, never banned
	CFRefresh              bool     // refresh Cloudflare ranges daily from the API
	CFAccountID            string   // Cloudflare IP list sync (optional)
	CFBanListID            string
	CFBanListToken         string
	OriginCertFile         string // public listener TLS certificate (Cloudflare Origin CA), PEM
	OriginKeyFile          string

	// ACME for the raw NATS hostname (PL-EDGE-006)
	ACMEEnabled      bool
	ACMEDirectory    string
	ACMEEmail        string
	ACMEDNSProvider  string // cloudflare | challtestsrv (dev/tests)
	ACMECFToken      string
	ACMEChallTestSrv string   // challtestsrv management URL
	ACMEDNSResolvers []string // resolvers lego uses for its own checks
	ACMECAFile       string   // extra roots for the ACME directory's TLS (pebble)
	ACMENATSAddr     string   // host:port to verify the served certificate on (default: from PULSE_NATS_URL)

	ShutdownTimeout time.Duration
}

// Getenv abstracts os.Getenv for tests.
type Getenv func(string) string

// Load reads the configuration from the environment.
func Load(getenv Getenv) (*Config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	e := envReader{get: getenv}
	c := &Config{
		Profile:  e.str("PULSE_PROFILE", ProfileProd),
		LogLevel: e.str("PULSE_LOG_LEVEL", "info"),
		Layout: bootstrap.Layout{
			DataDir: e.str("PULSE_DATA_DIR", "/data/pulsed"),
			NATSDir: e.str("PULSE_NATS_DIR", "/data/nats"),
		},
		NATSURL:                e.str("PULSE_NATS_URL", "tls://nats:4222"),
		NATSTLSName:            e.str("PULSE_NATS_TLS_NAME", "nats"),
		NATSClientListen:       e.str("PULSE_NATS_CLIENT_LISTEN", "0.0.0.0:4222"),
		NATSMonitorListen:      e.str("PULSE_NATS_MONITOR_LISTEN", "0.0.0.0:8222"),
		NATSWebsocketListen:    e.str("PULSE_NATS_WEBSOCKET_LISTEN", "0.0.0.0:8080"),
		NATSWebsocketURL:       e.str("PULSE_NATS_WEBSOCKET_URL", "http://nats:8080"),
		NATSMaxFileStore:       e.bytes("PULSE_NATS_MAX_FILE_STORE", 0),
		RawHost:                e.str("PULSE_RAW_HOST", ""),
		PublicHost:             e.str("PULSE_PUBLIC_HOST", ""),
		PublicNATSURL:          e.str("PULSE_PUBLIC_NATS_URL", ""),
		PublicURL:              e.str("PULSE_PUBLIC_URL", ""),
		EventsMaxBytes:         e.bytes("PULSE_EVENTS_MAX_BYTES", 10<<30),
		PublicAddr:             e.strAllowEmpty("PULSE_PUBLIC_ADDR", ":8443"),
		AdminAddr:              e.str("PULSE_ADMIN_ADDR", "127.0.0.1:8080"),
		MetricsAddr:            e.str("PULSE_METRICS_ADDR", "127.0.0.1:9464"),
		AdminPublishedLoopback: e.bool("PULSE_ADMIN_PUBLISHED_LOOPBACK", false),
		AdminPublic:            e.bool("PULSE_ADMIN_PUBLIC", false),
		AuthMode:               e.str("PULSE_AUTH_MODE", "none"),
		AdminHosts:             e.list("PULSE_ADMIN_HOSTS"),
		OutboundAllow:          e.list("PULSE_OUTBOUND_ALLOW"),
		AOPCAFile:              e.str("PULSE_AOP_CA_FILE", ""),
		BanThreshold:           e.int("PULSE_BAN_THRESHOLD", 5),
		BanWindow:              e.dur("PULSE_BAN_WINDOW", 10*time.Minute),
		BanDuration:            e.dur("PULSE_BAN_DURATION", time.Hour),
		BanMax:                 e.dur("PULSE_BAN_MAX", 30*24*time.Hour),
		BanAllow:               e.list("PULSE_BAN_ALLOW"),
		HostIPs:                e.list("PULSE_HOST_IPS"),
		CFRefresh:              e.bool("PULSE_CF_RANGES_REFRESH", true),
		CFAccountID:            e.str("PULSE_CF_ACCOUNT_ID", ""),
		CFBanListID:            e.str("PULSE_CF_BANLIST_ID", ""),
		OriginCertFile:         e.str("PULSE_ORIGIN_CERT_FILE", ""),
		OriginKeyFile:          e.str("PULSE_ORIGIN_KEY_FILE", ""),
		ShutdownTimeout:        e.dur("PULSE_SHUTDOWN_TIMEOUT", 20*time.Second),
		ACMEEnabled:            e.bool("PULSE_ACME_ENABLED", false),
		ACMEDirectory:          e.str("PULSE_ACME_DIRECTORY", "https://acme-v02.api.letsencrypt.org/directory"),
		ACMEEmail:              e.str("PULSE_ACME_EMAIL", ""),
		ACMEDNSProvider:        e.str("PULSE_ACME_DNS_PROVIDER", "cloudflare"),
		ACMEChallTestSrv:       e.str("PULSE_ACME_CHALLTESTSRV_URL", ""),
		ACMEDNSResolvers:       e.list("PULSE_ACME_DNS_RESOLVERS"),
		ACMECAFile:             e.str("PULSE_ACME_CA_FILE", ""),
		ACMENATSAddr:           e.str("PULSE_ACME_NATS_ADDR", ""),
	}
	if c.ACMEEnabled && c.ACMEDNSProvider == "cloudflare" {
		if tok, err := e.secret("PULSE_ACME_CF_TOKEN", c.Profile == ProfileProd); err != nil {
			e.errs = append(e.errs, err)
		} else {
			c.ACMECFToken = tok
		}
	}
	if tok, err := e.secret("PULSE_CF_BANLIST_TOKEN", c.Profile == ProfileProd); err != nil {
		e.errs = append(e.errs, err)
	} else {
		c.CFBanListToken = tok
	}
	if c.PublicNATSURL == "" {
		host := c.RawHost
		if host == "" {
			host = "localhost"
		}
		c.PublicNATSURL = "tls://" + host + ":4222"
	}
	if c.PublicURL == "" && c.PublicHost != "" {
		c.PublicURL = "https://" + c.PublicHost
	}
	mk, err := e.secret("PULSE_MASTER_KEY", c.Profile == ProfileProd)
	if err != nil {
		e.errs = append(e.errs, err)
	} else if mk != "" {
		c.MasterKey, err = secretbox.ParseMasterKey(mk)
		if err != nil {
			e.errs = append(e.errs, fmt.Errorf("PULSE_MASTER_KEY: %w", err))
		}
	}
	if len(e.errs) > 0 {
		return nil, errors.Join(e.errs...)
	}
	return c, c.Validate()
}

// Validate enforces profile rules (PL-OPS-004, PL-ADM-003).
func (c *Config) Validate() error {
	var errs []error
	switch c.Profile {
	case ProfileDev, ProfileProd:
	default:
		errs = append(errs, fmt.Errorf("PULSE_PROFILE must be dev or prod, got %q", c.Profile))
	}
	switch c.AuthMode {
	case "none":
	case "oidc":
		errs = append(errs, errors.New("PULSE_AUTH_MODE=oidc is not implemented yet (DECISIONS.md R-02)"))
	default:
		errs = append(errs, fmt.Errorf("PULSE_AUTH_MODE must be none or oidc, got %q", c.AuthMode))
	}
	if len(c.MasterKey) == 0 {
		errs = append(errs, errors.New("a master key is required: set PULSE_MASTER_KEY_FILE"))
	}
	if c.Profile == ProfileProd && c.AuthMode == "none" {
		if c.AdminPublic {
			errs = append(errs, errors.New("refusing PULSE_ADMIN_PUBLIC=true with PULSE_AUTH_MODE=none in the prod profile (ADR-0008)"))
		}
		if !isLoopback(c.AdminAddr) && !c.AdminPublishedLoopback {
			errs = append(errs, fmt.Errorf("refusing admin listener %q with PULSE_AUTH_MODE=none in the prod profile: bind a loopback "+
				"address, or bind inside a container and publish only on host loopback with PULSE_ADMIN_PUBLISHED_LOOPBACK=true (ADR-0008)", c.AdminAddr))
		}
	}
	if (c.OriginCertFile == "") != (c.OriginKeyFile == "") {
		errs = append(errs, errors.New("PULSE_ORIGIN_CERT_FILE and PULSE_ORIGIN_KEY_FILE go together"))
	}
	if c.Profile == ProfileProd && c.PublicAddr != "" && c.OriginCertFile == "" {
		errs = append(errs, errors.New("the prod profile refuses a plain-HTTP public listener: set PULSE_ORIGIN_CERT_FILE/PULSE_ORIGIN_KEY_FILE "+
			"or disable the listener with PULSE_PUBLIC_ADDR=off (PL-EDGE-010)"))
	}
	if c.Profile == ProfileProd && c.PublicAddr != "" && c.AOPCAFile == "" {
		errs = append(errs, errors.New("the prod profile requires Authenticated Origin Pulls on the public listener: set PULSE_AOP_CA_FILE (PL-EDGE-003)"))
	}
	if (c.CFAccountID != "" || c.CFBanListID != "" || c.CFBanListToken != "") && (c.CFAccountID == "" || c.CFBanListID == "" || c.CFBanListToken == "") {
		errs = append(errs, errors.New("the Cloudflare ban list sync needs PULSE_CF_ACCOUNT_ID, PULSE_CF_BANLIST_ID and PULSE_CF_BANLIST_TOKEN_FILE together"))
	}
	if c.ACMEEnabled {
		if c.RawHost == "" || c.RawHost == "localhost" {
			errs = append(errs, errors.New("PULSE_ACME_ENABLED needs PULSE_RAW_HOST set to the public NATS hostname"))
		}
		switch c.ACMEDNSProvider {
		case "cloudflare":
			if c.ACMECFToken == "" {
				errs = append(errs, errors.New("PULSE_ACME_DNS_PROVIDER=cloudflare needs PULSE_ACME_CF_TOKEN_FILE"))
			}
		case "challtestsrv":
			if c.Profile == ProfileProd {
				errs = append(errs, errors.New("the prod profile refuses PULSE_ACME_DNS_PROVIDER=challtestsrv (a test-only DNS server)"))
			}
			if c.ACMEChallTestSrv == "" {
				errs = append(errs, errors.New("PULSE_ACME_DNS_PROVIDER=challtestsrv needs PULSE_ACME_CHALLTESTSRV_URL"))
			}
		default:
			errs = append(errs, fmt.Errorf("PULSE_ACME_DNS_PROVIDER must be cloudflare or challtestsrv, got %q", c.ACMEDNSProvider))
		}
	}
	if c.BanThreshold < 2 || c.BanWindow < time.Minute || c.BanDuration < time.Minute || c.BanMax < c.BanDuration {
		errs = append(errs, errors.New("ban settings out of range (threshold ≥ 2, window ≥ 1m, duration ≥ 1m, max ≥ duration)"))
	}
	if c.EventsMaxBytes < 64<<20 {
		errs = append(errs, errors.New("PULSE_EVENTS_MAX_BYTES must be at least 64MiB"))
	}
	return errors.Join(errs...)
}

func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Dev reports whether the dev profile is active.
func (c *Config) Dev() bool { return c.Profile == ProfileDev }

type envReader struct {
	get  Getenv
	errs []error
}

// strAllowEmpty is str where "off" (or "none", "disabled") means explicitly empty, e.g. a disabled listener.
func (e *envReader) strAllowEmpty(k, def string) string {
	switch v := strings.TrimSpace(e.get(k)); v {
	case "":
		return def
	case "off", "none", "disabled":
		return ""
	default:
		return v
	}
}

func (e *envReader) list(k string) []string {
	var out []string
	for _, v := range strings.Split(e.get(k), ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, strings.ToLower(v))
		}
	}
	return out
}

func (e *envReader) str(k, def string) string {
	if v := strings.TrimSpace(e.get(k)); v != "" {
		return v
	}
	return def
}

func (e *envReader) int(k string, def int) int {
	v := strings.TrimSpace(e.get(k))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		e.errs = append(e.errs, fmt.Errorf("%s: %w", k, err))
	}
	return n
}

func (e *envReader) bool(k string, def bool) bool {
	v := e.get(k)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		e.errs = append(e.errs, fmt.Errorf("%s: %w", k, err))
	}
	return b
}

func (e *envReader) dur(k string, def time.Duration) time.Duration {
	v := e.get(k)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		e.errs = append(e.errs, fmt.Errorf("%s: %w", k, err))
	}
	return d
}

// bytes parses sizes like 1073741824, 10GiB, 512MiB, 60G.
func (e *envReader) bytes(k string, def int64) int64 {
	v := strings.TrimSpace(e.get(k))
	if v == "" {
		return def
	}
	n, err := ParseBytes(v)
	if err != nil {
		e.errs = append(e.errs, fmt.Errorf("%s: %w", k, err))
	}
	return n
}

// ParseBytes parses a byte size with an optional K/M/G/T (decimal) or KiB/MiB/GiB/TiB (binary) suffix.
func ParseBytes(v string) (int64, error) {
	units := []struct {
		suffix string
		mult   int64
	}{
		{"TiB", 1 << 40}, {"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10},
		{"TB", 1e12}, {"GB", 1e9}, {"MB", 1e6}, {"KB", 1e3},
		{"T", 1e12}, {"G", 1e9}, {"M", 1e6}, {"K", 1e3}, {"B", 1},
	}
	for _, u := range units {
		if strings.HasSuffix(v, u.suffix) {
			n, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(v, u.suffix)), 64)
			if err != nil || n < 0 {
				return 0, fmt.Errorf("invalid size %q", v)
			}
			return int64(n * float64(u.mult)), nil
		}
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid size %q", v)
	}
	return n, nil
}

// secret reads NAME_FILE (preferred) or NAME. With fileOnly, the plain variable is refused (prod).
func (e *envReader) secret(name string, fileOnly bool) (string, error) {
	if p := e.get(name + "_FILE"); p != "" {
		b, err := os.ReadFile(p) //nolint:gosec // operator-supplied secret path
		if err != nil {
			return "", fmt.Errorf("%s_FILE: %w", name, err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	if v := e.get(name); v != "" {
		if fileOnly {
			return "", fmt.Errorf("%s must be supplied as a file (%s_FILE) in the prod profile", name, name)
		}
		return v, nil
	}
	return "", nil
}

// Secret is the exported form of secret for feature packages (Cloudflare tokens, notifier tokens, …).
func Secret(getenv Getenv, name string, prod bool) (string, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	e := envReader{get: getenv}
	return e.secret(name, prod)
}
