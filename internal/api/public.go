package api

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"golang.org/x/time/rate"
)

// PublicPrefix is the public API base path.
const PublicPrefix = "/api/v1"

// Body limits on the public listener (PL-EDGE-009).
const (
	MaxPublicBody = 1 << 20
	MaxHookBody   = 5 << 20
)

func publicConfig(version string) huma.Config {
	c := huma.DefaultConfig("Pulse public API", version)
	c.Servers = []*huma.Server{{URL: PublicPrefix}}
	c.DocsPath = ""
	c.SchemasPath = ""
	c.OpenAPIPath = "/openapi"
	c.Info.Description = "Token-authenticated HTTP ingress for tools that cannot speak NATS. Send `Authorization: Bearer pulse_…`."
	c.Components.SecuritySchemes = map[string]*huma.SecurityScheme{"bearer": {Type: "http", Scheme: "bearer"}}
	return c
}

// NewPublicAPI registers public operations on mux.
func NewPublicAPI(mux *http.ServeMux, d *Deps) huma.API {
	api := humago.NewWithPrefix(mux, PublicPrefix, publicConfig(d.Version))
	huma.Register(api, huma.Operation{OperationID: "public-health", Method: http.MethodGet, Path: "/health", Summary: "Liveness", Tags: []string{"health"}},
		func(context.Context, *struct{}) (*struct {
			Body struct {
				Status string `json:"status"`
			}
		}, error) {
			out := &struct {
				Body struct {
					Status string `json:"status"`
				}
			}{}
			out.Body.Status = "ok"
			return out, nil
		})
	registerIngest(api, d)
	return api
}

// NewPublic builds the public listener handler: only /api/v1/*, /hooks/*, /ws and /healthz exist (PL-EDGE-001).
func NewPublic(d *Deps) http.Handler {
	mux := http.NewServeMux()
	NewPublicAPI(mux, d)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	allowed := func(p string) bool {
		return p == "/healthz" || p == "/ws" || strings.HasPrefix(p, PublicPrefix+"/") || strings.HasPrefix(p, "/hooks/")
	}
	limiter := newIPLimiter(20, 40)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowed(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		if !limiter.allow(remoteIP(r)) {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "rate limited", http.StatusTooManyRequests)
			return
		}
		limit := int64(MaxPublicBody)
		if strings.HasPrefix(r.URL.Path, "/hooks/") {
			limit = MaxHookBody
		}
		// Slightly above the Huma per-operation limit so Huma answers 413 itself; this reader is the hard backstop.
		r.Body = http.MaxBytesReader(w, r.Body, limit+4096)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		mux.ServeHTTP(w, r)
	})
	return accessLog(d.log(), "public", withClientIP(h))
}

// remoteIP is the TCP peer address. M8 replaces it with the Cloudflare-aware real IP (PL-EDGE-004).
func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ipLimiter is a per-IP token bucket with idle eviction.
type ipLimiter struct {
	mu    sync.Mutex
	r     rate.Limit
	burst int
	m     map[string]*ipEntry
	last  time.Time
}

type ipEntry struct {
	l    *rate.Limiter
	seen time.Time
}

func newIPLimiter(perSecond float64, burst int) *ipLimiter {
	return &ipLimiter{r: rate.Limit(perSecond), burst: burst, m: map[string]*ipEntry{}}
}

func (l *ipLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if now.Sub(l.last) > time.Minute {
		for k, e := range l.m {
			if now.Sub(e.seen) > 10*time.Minute {
				delete(l.m, k)
			}
		}
		l.last = now
	}
	e, ok := l.m[ip]
	if !ok {
		e = &ipEntry{l: rate.NewLimiter(l.r, l.burst)}
		l.m[ip] = e
	}
	e.seen = now
	return e.l.Allow()
}
