package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/aurimasniekis/go-pulse/internal/audit"
	"github.com/aurimasniekis/go-pulse/internal/auth"
	"github.com/aurimasniekis/go-pulse/internal/query"
	"github.com/aurimasniekis/go-pulse/internal/rollup"
	"github.com/aurimasniekis/go-pulse/internal/store"
	"github.com/aurimasniekis/go-pulse/internal/sysapi"
	"github.com/aurimasniekis/go-pulse/internal/tail"
)

func init() {
	// Every handler returns non-nil slices, so the spec (and the generated TS types) can say arrays are never null.
	huma.DefaultArrayNullable = false
}

// AdminPrefix is the admin API base path (PL-API-005).
const AdminPrefix = "/api/admin/v1"

// Deps are the services the APIs use. Spec generation passes a zero Deps: handlers must not run then.
type Deps struct {
	Log        *slog.Logger
	Version    string
	Profile    string
	DB         *store.DB
	NC         *nats.Conn
	JS         jetstream.JetStream
	Sys        *sysapi.Client
	Query      *query.Engine
	Tail       *tail.Manager
	Rollup     *rollup.Engine
	Auth       auth.Authenticator
	AdminHosts []string
	Ready      func() bool
}

func (d *Deps) log() *slog.Logger {
	if d.Log == nil {
		return slog.Default()
	}
	return d.Log
}

func (d *Deps) actor(ctx context.Context) audit.Actor {
	a := audit.Actor{Name: "admin"}
	if p := auth.From(ctx); p != nil {
		a.Name = p.Subject
	}
	if ip, ok := ctx.Value(clientIPKey{}).(string); ok {
		a.IP = ip
	}
	return a
}

type clientIPKey struct{}

func withClientIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), clientIPKey{}, remoteIP(r))))
	})
}

// internalErr logs err and returns an opaque 500 (PL-API-003).
func (d *Deps) internalErr(op string, err error) error {
	d.log().Error("api error", "op", op, "err", err)
	return huma.Error500InternalServerError("internal error")
}

func adminConfig(version string) huma.Config {
	c := huma.DefaultConfig("Pulse admin API", version)
	c.Servers = []*huma.Server{{URL: AdminPrefix}}
	c.DocsPath = "" // the docs UI loads third-party scripts, which the admin CSP forbids
	c.OpenAPIPath = "/openapi"
	c.Info.Description = "Loopback-only admin API for the Pulse dashboard. State-changing requests need the X-Pulse-CSRF header."
	return c
}

// NewAdminAPI registers every admin operation on mux and returns the Huma API.
func NewAdminAPI(mux *http.ServeMux, d *Deps) huma.API {
	api := humago.NewWithPrefix(mux, AdminPrefix, adminConfig(d.Version))
	registerSystem(api, d)
	registerStreams(api, d)
	registerEvents(api, d)
	registerRollups(api, d)
	return api
}

// NewAdmin builds the admin listener handler: API, UI and middleware (PL-ADM-001/004).
func NewAdmin(d *Deps) http.Handler {
	mux := http.NewServeMux()
	NewAdminAPI(mux, d)
	mux.Handle("/", uiHandler())
	var h http.Handler = mux
	h = csrf(h)
	h = auth.Middleware(d.Auth, h)
	h = withClientIP(h)
	h = securityHeaders(h)
	hosts := slices.Concat(DefaultAdminHosts, d.AdminHosts)
	h = hostCheck(hosts, h)
	return accessLog(d.log(), "admin", h)
}

// SystemInfo is the dashboard header/status payload.
type SystemInfo struct {
	Version  string `json:"version"`
	Profile  string `json:"profile"`
	AuthMode string `json:"auth_mode"`
	Ready    bool   `json:"ready"`
	NATS     struct {
		Connected  bool    `json:"connected"`
		ServerName string  `json:"server_name"`
		Version    string  `json:"version"`
		RTTMillis  float64 `json:"rtt_ms"`
	} `json:"nats"`
	JetStream *sysapi.Jsz `json:"jetstream,omitempty"`
}

func registerSystem(api huma.API, d *Deps) {
	huma.Register(api, huma.Operation{
		OperationID: "get-system-info", Method: http.MethodGet, Path: "/system/info", Summary: "Server status", Tags: []string{"system"},
	}, func(ctx context.Context, _ *struct{}) (*struct{ Body SystemInfo }, error) {
		out := &struct{ Body SystemInfo }{}
		b := &out.Body
		b.Version, b.Profile = d.Version, d.Profile
		if d.Auth != nil {
			b.AuthMode = d.Auth.Mode()
		}
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
		}
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
