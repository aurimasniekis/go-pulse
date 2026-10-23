package edge

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-pulse/internal/certs"
)

func TestRealIP(t *testing.T) {
	cf, _ := NewRanges(DefaultCloudflare)
	x := &RealIP{Cloudflare: cf, RequireAOP: true}
	verified := &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{{}}}}
	cases := []struct {
		name   string
		peer   string
		tls    *tls.ConnectionState
		header string
		want   string
	}{
		{"cloudflare + aop", "172.64.1.2:443", verified, "203.0.113.9", "203.0.113.9"},
		{"cloudflare v6 + aop", "[2606:4700::1]:443", verified, "2001:db8::5", "2001:db8::5"},
		{"cloudflare without aop", "172.64.1.2:443", &tls.ConnectionState{}, "203.0.113.9", "172.64.1.2"},
		{"cloudflare without tls", "172.64.1.2:443", nil, "203.0.113.9", "172.64.1.2"},
		{"not cloudflare", "198.51.100.7:5555", verified, "203.0.113.9", "198.51.100.7"},
		{"bad header", "172.64.1.2:443", verified, "not-an-ip", "172.64.1.2"},
		{"mapped peer", "[::ffff:198.51.100.7]:1", nil, "", "198.51.100.7"},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr, r.TLS = c.peer, c.tls
		if c.header != "" {
			r.Header.Set("CF-Connecting-IP", c.header)
		}
		if got := x.ClientIP(r); got != c.want {
			t.Errorf("%s: %s want %s", c.name, got, c.want)
		}
	}
	if !cf.Contains(netip.MustParseAddr("104.16.0.1")) || cf.Contains(netip.MustParseAddr("8.8.8.8")) {
		t.Fatal("ranges")
	}
}

func writeFile(t *testing.T, p string, b []byte) {
	t.Helper()
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// The local equivalent of `curl --cert`: only clients with a certificate from the AOP CA complete the handshake.
func TestAuthenticatedOriginPulls(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	originCA, _ := certs.NewCA("origin CA (stand-in for Cloudflare Origin CA)", now)
	origin, _ := originCA.IssueServer([]string{"events.example.test", "127.0.0.1"}, time.Hour, now)
	aopCA, _ := certs.NewCA("pulse AOP CA", now)
	cfClient, _ := aopCA.IssueClient("cloudflare", time.Hour, now)
	otherCA, _ := certs.NewCA("someone else", now)
	stranger, _ := otherCA.IssueClient("stranger", time.Hour, now)
	certFile, keyFile, aopFile := filepath.Join(dir, "o.crt"), filepath.Join(dir, "o.key"), filepath.Join(dir, "aop.crt")
	writeFile(t, certFile, origin.CertPEM)
	writeFile(t, keyFile, origin.KeyPEM)
	writeFile(t, aopFile, aopCA.CertPEM)

	reloader, err := NewCertReloader(certFile, keyFile, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ServerTLS(reloader, aopFile)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, map[bool]string{true: "aop-ok", false: "no-aop"}[AOPVerified(r)])
	}))
	srv.TLS = cfg
	srv.StartTLS()
	defer srv.Close()

	roots := x509.NewCertPool()
	roots.AddCert(originCA.Cert)
	call := func(leaf *certs.Leaf) (string, error) {
		tc := &tls.Config{RootCAs: roots, ServerName: "events.example.test", MinVersion: tls.VersionTLS12}
		if leaf != nil {
			pair, err := tls.X509KeyPair(leaf.CertPEM, leaf.KeyPEM)
			if err != nil {
				return "", err
			}
			tc.Certificates = []tls.Certificate{pair}
		}
		c := &http.Client{Transport: &http.Transport{TLSClientConfig: tc}, Timeout: 3 * time.Second}
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
		resp, err := c.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b), nil
	}
	if body, err := call(cfClient); err != nil || body != "aop-ok" {
		t.Fatalf("cloudflare client cert: %q %v", body, err)
	}
	if _, err := call(nil); err == nil {
		t.Fatal("handshake without a client certificate succeeded")
	}
	if _, err := call(stranger); err == nil {
		t.Fatal("client certificate from another CA accepted")
	}
}

func TestCertReloader(t *testing.T) {
	dir := t.TempDir()
	ca, _ := certs.NewCA("ca", time.Now())
	a, _ := ca.IssueServer([]string{"a.test"}, time.Hour, time.Now())
	certFile, keyFile := filepath.Join(dir, "c"), filepath.Join(dir, "k")
	writeFile(t, certFile, a.CertPEM)
	writeFile(t, keyFile, a.KeyPEM)
	r, err := NewCertReloader(certFile, keyFile, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.every = 0
	b, _ := ca.IssueServer([]string{"b.test"}, time.Hour, time.Now())
	later := time.Now().Add(2 * time.Second)
	writeFile(t, keyFile, b.KeyPEM)
	writeFile(t, certFile, b.CertPEM)
	_ = os.Chtimes(certFile, later, later)
	c, _ := r.GetCertificate(nil)
	if c.Leaf.DNSNames[0] != "b.test" {
		t.Fatalf("not reloaded: %v", c.Leaf.DNSNames)
	}
	// A broken pair keeps the previous certificate.
	writeFile(t, certFile, []byte("garbage"))
	even := later.Add(2 * time.Second)
	_ = os.Chtimes(certFile, even, even)
	c, err = r.GetCertificate(nil)
	if err != nil || c.Leaf.DNSNames[0] != "b.test" {
		t.Fatalf("broken reload replaced the certificate: %v", err)
	}
}

func TestWSProxyRewritesForwardedFor(t *testing.T) {
	var seen http.Header
	var path string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, path = r.Header.Clone(), r.URL.Path
	}))
	defer backend.Close()
	u, _ := url.Parse(backend.URL)
	p := WSProxy(u, func(*http.Request) string { return "203.0.113.50" })
	front := httptest.NewServer(p)
	defer front.Close()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, front.URL+"/ws", nil)
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 5.6.7.8")
	req.Header.Set("CF-Connecting-IP", "9.9.9.9")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := seen.Values("X-Forwarded-For"); len(got) != 1 || got[0] != "203.0.113.50" {
		t.Fatalf("X-Forwarded-For %v", got)
	}
	if seen.Get("CF-Connecting-IP") != "" || path != "/" && path != "" {
		t.Fatalf("leaked header or path %q %v", path, seen)
	}
}

func TestFetchCloudflare(t *testing.T) {
	ok := `{"success":true,"result":{"ipv4_cidrs":["173.245.48.0/20","103.21.244.0/22","103.22.200.0/22","103.31.4.0/22","141.101.64.0/18"],"ipv6_cidrs":["2400:cb00::/32","2606:4700::/32"]}}`
	for name, c := range map[string]struct {
		body string
		ok   bool
	}{
		"good":    {ok, true},
		"small":   {`{"success":true,"result":{"ipv4_cidrs":["1.1.1.0/24"],"ipv6_cidrs":[]}}`, false},
		"failure": {`{"success":false}`, false},
		"bad":     {strings.Replace(ok, "103.31.4.0/22", "nope", 1), false},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, c.body) }))
		got, err := FetchCloudflare(context.Background(), srv.Client(), srv.URL)
		srv.Close()
		if (err == nil) != c.ok || (c.ok && len(got) != 7) {
			t.Errorf("%s: %v %v", name, got, err)
		}
	}
}
