package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"runtime"
	"slices"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/aurimasniekis/go-pulse/internal/acme"
	"github.com/aurimasniekis/go-pulse/internal/alerts"
	"github.com/aurimasniekis/go-pulse/internal/audit"
	"github.com/aurimasniekis/go-pulse/internal/backup"
	"github.com/aurimasniekis/go-pulse/internal/ban"
	"github.com/aurimasniekis/go-pulse/internal/clients"
	"github.com/aurimasniekis/go-pulse/internal/hooks"
	"github.com/aurimasniekis/go-pulse/internal/oidc"
	"github.com/aurimasniekis/go-pulse/internal/outbound"
	"github.com/aurimasniekis/go-pulse/internal/query"
	"github.com/aurimasniekis/go-pulse/internal/rollup"
	"github.com/aurimasniekis/go-pulse/internal/store"
	"github.com/aurimasniekis/go-pulse/internal/sysapi"
	"github.com/aurimasniekis/go-pulse/internal/tail"
	"github.com/aurimasniekis/go-pulse/internal/tokens"
	"github.com/aurimasniekis/go-pulse/internal/users"
)

func init() {
	// Every handler returns non-nil slices, so the spec (and the generated TS types) can say arrays are never null.
	huma.DefaultArrayNullable = false
}

// AdminPrefix is the admin API base path (PL-API-005).
const AdminPrefix = "/api/admin/v1"

// Deps are the services the APIs use. Spec generation passes a zero Deps: handlers must not run then.
type Deps struct {
	Log     *slog.Logger
	Version string
	Profile string
	DB      *store.DB
	NC      *nats.Conn
	JS      jetstream.JetStream
	Sys     *sysapi.Client
	Query   *query.Engine
	Tail    *tail.Manager
	Rollup  *rollup.Engine
	Tokens  *tokens.Service
	Clients *clients.Service
	Bans    *ban.Service
	Edge    EdgeInfo
	// ClientIP returns the real client address of a public request (Cloudflare-aware). Defaults to the TCP peer.
	ClientIP func(*http.Request) string
	// WSTarget is the NATS websocket URL /ws proxies to; nil disables /ws.
	WSTarget *url.URL
	Hooks    *hooks.Service
	Outbound *outbound.Service
	// Connection details shown in snippets and the system page.
	PublicNATSURL string
	PublicURL     string
	CACertPEM     string
	// OnAuthFailure is told about failed token authentication (the ban detector, M8). May be nil.
	OnAuthFailure func(ip, source string)
	// Users and OIDC are dashboard sign-in (ADR-0038). ReauthWindow is shown to the dashboard.
	Users        *users.Service
	OIDC         *oidc.Service
	ReauthWindow time.Duration
	// AdminHosts are extra Host names the loopback admin listener accepts.
	AdminHosts []string
	// AdminPublicHost serves the dashboard on the public listener under this Host ("" = not exposed). It may equal the
	// public API host (path routing) or be its own hostname.
	AdminPublicHost string
	// PublicAPIHost is the public API hostname; with a separate AdminPublicHost each host gets only its own routes.
	PublicAPIHost string
	// PublicTLS: the public listener serves TLS (__Host- cookies with Secure).
	PublicTLS bool
	Ready     func() bool
	// CertSources are the certificates shown on the System page; ACME returns the renewal status (nil when disabled).
	CertSources []CertSource
	ACME        func() acme.Status
	Alerts      *alerts.Engine
	Started     time.Time
	// AccountNames maps account public keys to display names (SYS, APP).
	AccountNames map[string]string
	Backups      *backup.Service
	BackupStore  BackupStore
	Config       []ConfigSection // the Settings page (built from the configuration at start)

	ssoTests ssoTests
}

func (d *Deps) accountNames() map[string]string {
	if d.AccountNames == nil {
		return map[string]string{}
	}
	return d.AccountNames
}

func (d *Deps) log() *slog.Logger {
	if d.Log == nil {
		return slog.Default()
	}
	return d.Log
}

func (d *Deps) actor(ctx context.Context) audit.Actor {
	ip, _ := ctx.Value(clientIPKey{}).(string)
	if p := principalFrom(ctx); p != nil {
		a := users.Actor(p.User, ip)
		if p.Method == users.MethodToken {
			a.Name += " (token " + p.TokenID + ")"
		}
		return a
	}
	return audit.Actor{Name: "anonymous", IP: ip}
}

type clientIPKey struct{}

func withClientIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), clientIPKey{}, remoteIP(r))))
	})
}

// internalErr logs err and returns an opaque 500 (PL-API-003).
func (d *Deps) internalErr(op string, err error) error {
	if errors.Is(err, context.Canceled) {
		// The client went away (navigation, closed tab): not a server error.
		return huma.NewError(499, "client closed request")
	}
	d.log().Error("api error", "op", op, "err", err)
	return huma.Error500InternalServerError("internal error")
}

func adminConfig(version string) huma.Config {
	c := huma.DefaultConfig("Pulse admin API", version)
	c.Servers = []*huma.Server{{URL: AdminPrefix}}
	c.DocsPath = "" // the docs UI loads third-party scripts, which the admin CSP forbids
	c.OpenAPIPath = "/openapi"
	c.Info.Description = "Admin API for the Pulse dashboard. Sign in for a session cookie (state-changing requests then need the session's " +
		"CSRF token in X-Pulse-CSRF), or, on the loopback listener only, send a personal access token as Authorization: Bearer pulse_pat_…."
	return c
}

// NewAdminAPI registers every admin operation on mux and returns the Huma API.
func NewAdminAPI(mux *http.ServeMux, d *Deps) huma.API {
	api := humago.NewWithPrefix(mux, AdminPrefix, adminConfig(d.Version))
	api.UseMiddleware(d.authorizeOps(api))
	registerAuth(api, d)
	registerAccount(api, d)
	registerUsers(api, d)
	registerSSO(api, d)
	registerSystem(api, d)
	registerCertificates(api, d)
	registerStreams(api, d)
	registerEvents(api, d)
	registerRollups(api, d)
	registerClients(api, d)
	registerTokens(api, d)
	registerAudit(api, d)
	registerWebhooks(api, d)
	registerSecurity(api, d)
	registerAlerts(api, d)
	registerBackups(api, d)
	registerKV(api, d)
	registerConfig(api, d)
	registerConnections(api, d)
	return api
}

// NewAdmin builds the loopback admin listener handler: API, SSO endpoints, UI and middleware (PL-ADM-001/004).
func NewAdmin(d *Deps) http.Handler {
	h := d.adminHandler(listener{kind: ListenerLoopback})
	h = withClientIP(h)
	h = hostCheck(slices.Concat(DefaultAdminHosts, d.AdminHosts), h)
	return accessLog(d.log(), "admin", h)
}

// adminHandler is the dashboard for one listener; the caller adds the Host check and the client IP.
func (d *Deps) adminHandler(l listener) http.Handler {
	mux := http.NewServeMux()
	NewAdminAPI(mux, d)
	d.oidcHandlers(mux)
	mux.Handle("/", uiHandler())
	var h http.Handler = mux
	h = withRequest(h)
	h = d.authenticate(l, h)
	return securityHeaders(l, h)
}

// SystemInfo is the dashboard header/status payload.
type SystemInfo struct {
	Version string `json:"version"`
	Profile string `json:"profile"`
	Ready   bool   `json:"ready"`
	NATS    struct {
		Connected  bool    `json:"connected"`
		ServerName string  `json:"server_name"`
		Version    string  `json:"version"`
		RTTMillis  float64 `json:"rtt_ms"`
	} `json:"nats"`
	JetStream *sysapi.Jsz  `json:"jetstream,omitempty"`
	Server    *ServerStats `json:"server,omitempty" doc:"NATS server statistics (VARZ)"`
	Runtime   RuntimeStats `json:"runtime"`
	Connect   struct {
		NATSURL   string `json:"nats_url" doc:"Where tools connect (raw NATS TLS)"`
		PublicURL string `json:"public_url" doc:"Base URL of the public HTTP API"`
		CACert    string `json:"ca_cert" doc:"Internal CA (PEM); clients need it while 4222 serves the bootstrap certificate"`
	} `json:"connect"`
}

// ServerStats is the NATS VARZ subset shown on the System page.
type ServerStats struct {
	Start         time.Time `json:"start"`
	Uptime        string    `json:"uptime"`
	Go            string    `json:"go"`
	Mem           int64     `json:"mem_bytes"`
	CPU           float64   `json:"cpu_percent"`
	Cores         int       `json:"cores"`
	Connections   int       `json:"connections"`
	TotalConns    uint64    `json:"total_connections"`
	Subscriptions uint32    `json:"subscriptions"`
	InMsgs        int64     `json:"in_msgs"`
	OutMsgs       int64     `json:"out_msgs"`
	InBytes       int64     `json:"in_bytes"`
	OutBytes      int64     `json:"out_bytes"`
	SlowConsumers int64     `json:"slow_consumers"`
	MaxPayload    int       `json:"max_payload"`
	ConfigLoaded  time.Time `json:"config_load_time"`
}

// RuntimeStats describes the pulsed process.
type RuntimeStats struct {
	Started    time.Time `json:"started,omitzero"`
	GoVersion  string    `json:"go_version"`
	Goroutines int       `json:"goroutines"`
	HeapBytes  uint64    `json:"heap_bytes"`
	SysBytes   uint64    `json:"sys_bytes" doc:"Memory obtained from the OS"`
	GCCycles   uint32    `json:"gc_cycles"`
	CPUs       int       `json:"cpus"`
}

func registerSystem(api huma.API, d *Deps) {
	huma.Register(api, huma.Operation{
		OperationID: "get-system-info", Method: http.MethodGet, Path: "/system/info", Summary: "Server status", Tags: []string{"system"},
	}, func(ctx context.Context, _ *struct{}) (*struct{ Body SystemInfo }, error) {
		out := &struct{ Body SystemInfo }{}
		b := &out.Body
		b.Version, b.Profile = d.Version, d.Profile
		b.Connect.NATSURL, b.Connect.PublicURL, b.Connect.CACert = d.PublicNATSURL, d.PublicURL, d.CACertPEM
		if d.Ready != nil {
			b.Ready = d.Ready()
		}
		if d.NC != nil && d.NC.IsConnected() {
			b.NATS.Connected = true
			b.NATS.ServerName = d.NC.ConnectedServerName()
			b.NATS.Version = d.NC.ConnectedServerVersion()
			if rtt, err := d.NC.RTT(); err == nil {
				b.NATS.RTTMillis = float64(rtt.Microseconds()) / 1000
			}
		}
		if d.Sys != nil {
			if jsz, err := d.Sys.Jsz(ctx); err == nil {
				b.JetStream = jsz
			}
			if v, err := d.Sys.Varz(ctx); err == nil {
				b.Server = &ServerStats{Start: v.Start, Uptime: v.Uptime, Go: v.GoVersion, Mem: v.Mem, CPU: v.CPU, Cores: v.Cores,
					Connections: v.Connections, TotalConns: v.TotalConns, Subscriptions: v.Subscriptions, InMsgs: v.InMsgs, OutMsgs: v.OutMsgs,
					InBytes: v.InBytes, OutBytes: v.OutBytes, SlowConsumers: v.SlowConsumers, MaxPayload: v.MaxPayload, ConfigLoaded: v.ConfigLoadTime}
			}
		}
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		b.Runtime = RuntimeStats{Started: d.Started, GoVersion: runtime.Version(), Goroutines: runtime.NumGoroutine(), HeapBytes: ms.HeapAlloc,
			SysBytes: ms.Sys, GCCycles: ms.NumGC, CPUs: runtime.NumCPU()}
		return out, nil
	})
}

func mapQueryErr(d *Deps, op string, err error) error {
	switch {
	case errors.Is(err, query.ErrRequest), errors.Is(err, query.ErrCursor):
		return huma.Error400BadRequest(err.Error())
	case errors.Is(err, query.ErrBusy), errors.Is(err, tail.ErrTooMany):
		return huma.Error429TooManyRequests(err.Error())
	case errors.Is(err, jetstream.ErrStreamNotFound):
		return huma.Error404NotFound("stream not found")
	case errors.Is(err, jetstream.ErrMsgNotFound):
		return huma.Error404NotFound("message not found")
	case errors.Is(err, context.Canceled):
		return huma.NewError(499, "client closed request")
	default:
		return d.internalErr(op, err)
	}
}

// ConfigItem is one effective setting; secrets show only whether they are set.
type ConfigItem struct {
	Env    string `json:"env"`
	Value  string `json:"value"`
	Secret bool   `json:"secret,omitempty" doc:"The value is a secret: only set/not set is shown"`
}

// ConfigSection groups settings for the Settings page.
type ConfigSection struct {
	Title string       `json:"title"`
	Items []ConfigItem `json:"items"`
}

func registerConfig(api huma.API, d *Deps) {
	huma.Register(api, huma.Operation{OperationID: "get-config", Method: http.MethodGet, Path: "/system/config",
		Summary: "Effective configuration (no secret values)", Tags: []string{"system"}},
		func(_ context.Context, _ *struct{}) (*struct{ Body []ConfigSection }, error) {
			out := d.Config
			if out == nil {
				out = []ConfigSection{}
			}
			return &struct{ Body []ConfigSection }{out}, nil
		})
}
