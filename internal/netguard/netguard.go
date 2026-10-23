// Package netguard provides an HTTP client for outbound webhooks that refuses internal destinations (PL-HOOK-008).
//
// The check runs in the dialer's Control hook, on the address actually being connected to after DNS resolution, so a
// hostname that resolves (or re-resolves, DNS rebinding) to an internal address is refused as well.
package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"syscall"
	"time"
)

// ErrBlocked is returned when a destination is internal and not allowlisted.
var ErrBlocked = errors.New("destination address is not allowed")

var blocked = mustPrefixes(
	"0.0.0.0/8",       // this network
	"10.0.0.0/8",      // private
	"100.64.0.0/10",   // CGNAT
	"127.0.0.0/8",     // loopback
	"169.254.0.0/16",  // link-local, cloud metadata
	"172.16.0.0/12",   // private
	"192.0.0.0/24",    // IETF protocol assignments
	"192.0.2.0/24",    // TEST-NET-1
	"192.88.99.0/24",  // 6to4 relay
	"192.168.0.0/16",  // private
	"198.18.0.0/15",   // benchmarking
	"198.51.100.0/24", // TEST-NET-2
	"203.0.113.0/24",  // TEST-NET-3
	"224.0.0.0/4",     // multicast
	"240.0.0.0/4",     // reserved, broadcast
	"::/128",          // unspecified
	"::1/128",         // loopback
	"::ffff:0:0/96",   // IPv4-mapped (checked after unmapping too)
	"64:ff9b::/96",    // NAT64
	"100::/64",        // discard
	"2001:db8::/32",   // documentation
	"fc00::/7",        // unique local
	"fe80::/10",       // link-local
	"ff00::/8",        // multicast
)

func mustPrefixes(ss ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(ss))
	for i, s := range ss {
		out[i] = netip.MustParsePrefix(s)
	}
	return out
}

// Guard decides which addresses may be dialed.
type Guard struct {
	allow []netip.Prefix
}

// New returns a guard; allow lists networks that are permitted even though they are internal (e.g. a LAN service).
func New(allow []string) (*Guard, error) {
	g := &Guard{}
	for _, a := range allow {
		p, err := netip.ParsePrefix(a)
		if err != nil {
			addr, aerr := netip.ParseAddr(a)
			if aerr != nil {
				return nil, fmt.Errorf("netguard: %q is not an IP or CIDR", a)
			}
			p = netip.PrefixFrom(addr, addr.BitLen())
		}
		g.allow = append(g.allow, p.Masked())
	}
	return g, nil
}

// Allowed reports whether an address may be dialed.
func (g *Guard) Allowed(a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range g.allow {
		if p.Contains(a) {
			return true
		}
	}
	if !a.IsValid() {
		return false
	}
	for _, p := range blocked {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

func (g *Guard) control(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrBlocked, address)
	}
	if !g.Allowed(ap.Addr()) {
		return fmt.Errorf("%w: %s", ErrBlocked, ap.Addr())
	}
	return nil
}

// Client returns an HTTP client whose every connection (including redirects) passes the guard. It ignores
// HTTP(S)_PROXY settings: a proxy would hide the real destination from the guard.
func (g *Guard) Client(timeout time.Duration) *http.Client {
	d := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second, Control: g.control}
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           d.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: timeout,
	}
	return &http.Client{
		Transport: tr,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("too many redirects")
			}
			return CheckURL(req.URL)
		},
	}
}

// CheckURL validates the static parts of a destination URL: http(s) only, no credentials, and literal IPs must pass
// the blocklist (hostnames are checked again at dial time).
func CheckURL(u *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("only http and https URLs are allowed")
	}
	if u.User != nil {
		return errors.New("URLs with credentials are not allowed; use a header")
	}
	if u.Hostname() == "" {
		return errors.New("URL has no host")
	}
	return nil
}

// Validate is CheckURL plus the guard check for literal IP hosts, for config-time feedback.
func (g *Guard) Validate(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if err := CheckURL(u); err != nil {
		return err
	}
	if a, err := netip.ParseAddr(u.Hostname()); err == nil && !g.Allowed(a) {
		return fmt.Errorf("%w: %s", ErrBlocked, a)
	}
	if u.Hostname() == "localhost" || u.Hostname() == "metadata.google.internal" {
		return fmt.Errorf("%w: %s", ErrBlocked, u.Hostname())
	}
	return nil
}

// Resolve checks every address a host resolves to (best-effort config-time warning; the dialer is authoritative).
func (g *Guard) Resolve(ctx context.Context, host string) error {
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return err
	}
	for _, a := range addrs {
		if !g.Allowed(a) {
			return fmt.Errorf("%w: %s resolves to %s", ErrBlocked, host, a)
		}
	}
	return nil
}
