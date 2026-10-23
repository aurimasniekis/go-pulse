package edge

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// RealIP decides the client IP of a public request (PL-EDGE-004): CF-Connecting-IP is trusted only when the TCP peer
// is a Cloudflare address AND the connection presented a client certificate that verified against the AOP CA.
type RealIP struct {
	Cloudflare *Ranges
	// RequireAOP must be true in prod; dev may disable it to test behind a local proxy.
	RequireAOP bool
}

func peer(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	a, _ := netip.ParseAddr(host)
	return a.Unmap()
}

// AOPVerified reports whether the TLS connection carried a verified client certificate.
func AOPVerified(r *http.Request) bool {
	return r.TLS != nil && len(r.TLS.VerifiedChains) > 0
}

// FromCloudflare reports whether the request arrived through Cloudflare under the trust rules.
func (x *RealIP) FromCloudflare(r *http.Request) bool {
	p := peer(r)
	if !p.IsValid() || x.Cloudflare == nil || !x.Cloudflare.Contains(p) {
		return false
	}
	return !x.RequireAOP || AOPVerified(r)
}

// ClientIP returns the real client address as a string.
func (x *RealIP) ClientIP(r *http.Request) string {
	if x.FromCloudflare(r) {
		if a, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get("CF-Connecting-IP"))); err == nil {
			return a.Unmap().String()
		}
	}
	p := peer(r)
	if !p.IsValid() {
		return ""
	}
	return p.String()
}
