// Package api serves the admin API + UI and the public API with Huma v2 on net/http (PL-API-001..005, ADR-0013).
package api

import (
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aurimasniekis/go-pulse/internal/metrics"
)

// CSRFHeader carries the session's CSRF token on state-changing admin requests (PL-ADM-004, ADR-0038). Browsers cannot
// add custom headers to cross-site requests, and CORS is never enabled.
const CSRFHeader = "X-Pulse-CSRF"

const adminCSP = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; " +
	"font-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'; object-src 'none'"

func securityHeaders(l listener, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		if l.secure {
			h.Set("Strict-Transport-Security", "max-age=63072000")
		}
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/auth/") {
			h.Set("Cache-Control", "no-store")
		}
		h.Set("Content-Security-Policy", adminCSP)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

func normalizeHost(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.Trim(strings.ToLower(host), "[]")
}

// hostCheck rejects requests whose Host header is not an allowed admin host. With auth none on a loopback listener,
// this is what stops DNS-rebinding pages from talking to the admin API (THREAT_MODEL T-21).
func hostCheck(allowed []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !slices.Contains(allowed, normalizeHost(r.Host)) {
			http.Error(w, "host not allowed", http.StatusMisdirectedRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// DefaultAdminHosts are accepted Host names for the admin listener (plus PULSE_ADMIN_HOSTS).
var DefaultAdminHosts = []string{"127.0.0.1", "localhost", "::1"}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// methodLabel bounds the method label to the standard methods.
func methodLabel(m string) string {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions:
		return m
	default:
		return "other"
	}
}

func accessLog(log *slog.Logger, listener string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		lvl := slog.LevelDebug
		if sw.status >= 500 {
			lvl = slog.LevelWarn
		}
		took := time.Since(start)
		route := metrics.Route(r.URL.Path)
		metrics.HTTPRequests.WithLabelValues(listener, route, methodLabel(r.Method), strconv.Itoa(sw.status)).Inc()
		metrics.HTTPDuration.WithLabelValues(listener, route).Observe(took.Seconds())
		log.Log(r.Context(), lvl, "http", "listener", listener, "method", r.Method, "path", r.URL.Path, "status", sw.status,
			"ms", took.Milliseconds())
	})
}
