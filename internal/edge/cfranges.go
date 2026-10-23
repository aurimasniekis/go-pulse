// Package edge implements the public listener's edge concerns: Cloudflare ranges, the real client IP (PL-EDGE-004),
// TLS with the origin certificate and Authenticated Origin Pulls (PL-EDGE-002/003), and the /ws proxy (PL-EDGE-005).
package edge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"sync/atomic"
	"time"
)

// DefaultCloudflare are Cloudflare's published ranges (https://www.cloudflare.com/ips/, verified 2026-10-24,
// API etag 38f79d050aa027e3be3865e495dcc9bc). Refreshed at runtime when enabled (PL-EDGE-008).
var DefaultCloudflare = []string{
	"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22", "141.101.64.0/18", "108.162.192.0/18",
	"190.93.240.0/20", "188.114.96.0/20", "197.234.240.0/22", "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
	"104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
	"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32", "2405:8100::/32", "2a06:98c0::/29", "2c0f:f248::/32",
}

// Ranges is a swappable set of networks.
type Ranges struct {
	v       atomic.Pointer[[]netip.Prefix]
	updated atomic.Int64
}

// NewRanges parses CIDRs.
func NewRanges(cidrs []string) (*Ranges, error) {
	r := &Ranges{}
	if err := r.Set(cidrs); err != nil {
		return nil, err
	}
	return r, nil
}

// Set replaces the ranges.
func (r *Ranges) Set(cidrs []string) error {
	ps := make([]netip.Prefix, 0, len(cidrs))
	for _, c := range cidrs {
		p, err := netip.ParsePrefix(c)
		if err != nil {
			return fmt.Errorf("edge: bad CIDR %q: %w", c, err)
		}
		ps = append(ps, p.Masked())
	}
	r.v.Store(&ps)
	r.updated.Store(time.Now().Unix())
	return nil
}

// Contains reports whether a is in the ranges.
func (r *Ranges) Contains(a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range *r.v.Load() {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// List returns the ranges as strings.
func (r *Ranges) List() []string {
	ps := *r.v.Load()
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.String()
	}
	return out
}

// Updated is when the ranges were last set.
func (r *Ranges) Updated() time.Time { return time.Unix(r.updated.Load(), 0).UTC() }

// CloudflareIPsURL is Cloudflare's public endpoint for its ranges.
const CloudflareIPsURL = "https://api.cloudflare.com/client/v4/ips"

// FetchCloudflare downloads the current ranges. It refuses suspiciously small answers so a broken response cannot
// shrink the trusted set to nothing.
func FetchCloudflare(ctx context.Context, c *http.Client, url string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cloudflare ips: %s", resp.Status)
	}
	var body struct {
		Success bool `json:"success"`
		Result  struct {
			V4 []string `json:"ipv4_cidrs"`
			V6 []string `json:"ipv6_cidrs"`
		} `json:"result"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 1<<20)).Decode(&body); err != nil {
		return nil, err
	}
	if !body.Success || len(body.Result.V4) < 5 || len(body.Result.V6) < 2 {
		return nil, errors.New("cloudflare ips: unexpected or too small answer")
	}
	out := append(body.Result.V4, body.Result.V6...)
	for _, c := range out {
		if _, err := netip.ParsePrefix(c); err != nil {
			return nil, fmt.Errorf("cloudflare ips: bad CIDR %q", c)
		}
	}
	return out, nil
}
