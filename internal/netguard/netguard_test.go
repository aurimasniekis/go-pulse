package netguard

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

func TestAllowed(t *testing.T) {
	g, _ := New(nil)
	for _, s := range []string{"127.0.0.1", "10.1.2.3", "172.20.0.1", "192.168.1.1", "169.254.169.254", "100.64.1.1", "0.0.0.0",
		"::1", "fd00::1", "fe80::1", "::ffff:127.0.0.1", "224.0.0.1", "255.255.255.255"} {
		if g.Allowed(netip.MustParseAddr(s)) {
			t.Errorf("%s allowed", s)
		}
	}
	for _, s := range []string{"1.1.1.1", "140.82.112.3", "2606:4700:4700::1111"} {
		if !g.Allowed(netip.MustParseAddr(s)) {
			t.Errorf("%s blocked", s)
		}
	}
	lan, _ := New([]string{"192.168.1.0/24", "10.9.9.9"})
	if !lan.Allowed(netip.MustParseAddr("192.168.1.20")) || !lan.Allowed(netip.MustParseAddr("10.9.9.9")) || lan.Allowed(netip.MustParseAddr("10.9.9.8")) {
		t.Fatal("allowlist")
	}
	if _, err := New([]string{"nope"}); err == nil {
		t.Fatal("bad allow entry accepted")
	}
}

func TestClientRefusesLoopbackAtDialTime(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer srv.Close()
	g, _ := New(nil)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	resp, err := g.Client(time.Second).Do(req)
	if err == nil {
		resp.Body.Close()
		t.Fatal("loopback request succeeded")
	}
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("unexpected error: %v", err)
	}
	// "localhost" resolves to loopback: blocked at dial time too.
	req, _ = http.NewRequestWithContext(context.Background(), http.MethodGet, "http://localhost:"+srv.Listener.Addr().(interface{ String() string }).String()[len("127.0.0.1:"):], nil)
	if resp, err := g.Client(time.Second).Do(req); err == nil {
		resp.Body.Close()
		t.Fatal("localhost request succeeded")
	}
	ok, _ := New([]string{"127.0.0.0/8"})
	req, _ = http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	resp, err = ok.Client(time.Second).Do(req)
	if err != nil {
		t.Fatalf("allowlisted loopback: %v", err)
	}
	resp.Body.Close()
}

// Redirects are re-dialed through the same guarded dialer, so a redirect to an internal address fails like a direct
// request; CheckRedirect additionally enforces the URL rules and a hop limit.
func TestValidateURLs(t *testing.T) {
	g, _ := New(nil)
	if err := g.Validate("ftp://example.com/x"); err == nil {
		t.Fatal("ftp accepted")
	}
	if err := g.Validate("http://user:pw@example.com/"); err == nil {
		t.Fatal("credentials in URL accepted")
	}
	if err := g.Validate("http://169.254.169.254/latest/meta-data"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("metadata IP: %v", err)
	}
	if err := g.Validate("http://localhost:8080/"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("localhost: %v", err)
	}
	if err := g.Validate("https://hooks.example.com/x"); err != nil {
		t.Fatalf("public URL: %v", err)
	}
}
