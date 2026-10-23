package edge

import (
	"net/http"
	"net/http/httputil"
	"net/url"
)

// WSProxy forwards /ws to the NATS websocket listener (PL-EDGE-005). NATS trusts the first X-Forwarded-For value as the
// client address, so the proxy replaces the header with exactly one trusted value instead of appending to it.
func WSProxy(target *url.URL, clientIP func(*http.Request) string) http.Handler {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.URL.Path = target.Path
			pr.Out.URL.RawPath = ""
			pr.Out.Host = target.Host
			pr.Out.Header.Del("X-Forwarded-For")
			pr.Out.Header.Del("Forwarded")
			pr.Out.Header.Del("CF-Connecting-IP")
			if ip := clientIP(pr.In); ip != "" {
				pr.Out.Header.Set("X-Forwarded-For", ip)
			}
			pr.Out.Header.Del("X-Forwarded-Host")
			pr.Out.Header.Del("X-Forwarded-Proto")
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "upstream unavailable", http.StatusBadGateway)
		},
	}
}
