package acme

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-pulse/internal/certs"
	"github.com/aurimasniekis/go-pulse/internal/testutil/natstest"
)

type nopProvider struct{}

func (nopProvider) Present(string, string, string) error { return nil }
func (nopProvider) CleanUp(string, string, string) error { return nil }

func testKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func newManager(t *testing.T, now time.Time) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	m, err := New(Options{Domain: "r.pulse.test", Provider: nopProvider{}, AccountKey: testKey(t),
		CertFile: filepath.Join(dir, "acme.crt"), KeyFile: filepath.Join(dir, "acme.key"), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	return m, dir
}

func TestDue(t *testing.T) {
	now := time.Now()
	ca, err := certs.NewCA("test", now)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := newManager(t, now)
	if !m.Due() {
		t.Fatal("no certificate must be due")
	}
	write := func(names []string, validFor time.Duration) {
		leaf, err := ca.IssueServer(names, validFor, now)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(m.o.CertFile, leaf.CertPEM, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write([]string{"r.pulse.test"}, 90*24*time.Hour)
	if m.Due() {
		t.Fatal("a fresh certificate must not be due")
	}
	m.o.Now = func() time.Time { return now.Add(61 * 24 * time.Hour) }
	if !m.Due() {
		t.Fatal("a 90-day certificate with < 30 days left must be due")
	}
	m.o.Now = func() time.Time { return now }
	// Short-lived certificates renew in the last third of their lifetime, not on every check.
	write([]string{"r.pulse.test"}, 6*24*time.Hour)
	if m.Due() {
		t.Fatal("a fresh 6-day certificate must not be due")
	}
	m.o.Now = func() time.Time { return now.Add(5 * 24 * time.Hour) }
	if !m.Due() {
		t.Fatal("a 6-day certificate with 1 day left must be due")
	}
	m.o.Now = func() time.Time { return now }
	write([]string{"other.test"}, 90*24*time.Hour)
	if !m.Due() {
		t.Fatal("a certificate for another name must be due")
	}
}

func TestNewRequiresFields(t *testing.T) {
	if _, err := New(Options{Domain: "r.pulse.test"}); err == nil {
		t.Fatal("missing provider/key/paths accepted")
	}
}

func TestChallTestSrvProvider(t *testing.T) {
	var got []map[string]string
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		got, paths = append(got, body), append(paths, r.URL.Path)
	}))
	defer srv.Close()
	p := &ChallTestSrv{URL: srv.URL, HTTP: srv.Client()}
	if err := p.Present("r.pulse.test", "tok", "tok.thumb"); err != nil {
		t.Fatal(err)
	}
	if err := p.CleanUp("r.pulse.test", "tok", "tok.thumb"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(paths, ",") != "/set-txt,/clear-txt" {
		t.Fatalf("paths = %v", paths)
	}
	if got[0]["host"] != "_acme-challenge.r.pulse.test." || got[0]["value"] == "" || got[1]["host"] != got[0]["host"] {
		t.Fatalf("bodies = %v", got)
	}
}

func TestParseAccountKey(t *testing.T) {
	k := testKey(t)
	der, _ := x509.MarshalECPrivateKey(k)
	p := string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))
	got, err := ParseAccountKey(p)
	if err != nil {
		t.Fatal(err)
	}
	if !got.(*ecdsa.PrivateKey).Equal(k) {
		t.Fatal("key mismatch")
	}
	if _, err := ParseAccountKey(""); err == nil {
		t.Fatal("empty key accepted")
	}
}

// ServedSerial does the NATS INFO → TLS upgrade and sees the internal leaf for SNI "nats".
func TestServedSerialInternalLeaf(t *testing.T) {
	env := natstest.Start(t, natstest.Options{NoWebsocket: true})
	b, err := os.ReadFile(env.Layout.ServerCert())
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := certs.ParseLeaf(b)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(env.Keys.CACert))
	addr := strings.TrimPrefix(env.ClientURL, "tls://")
	got, err := ServedSerial(context.Background(), addr, "nats", roots)
	if err != nil {
		t.Fatal(err)
	}
	if got != certs.SerialHex(leaf) {
		t.Fatalf("served %s, want %s", got, certs.SerialHex(leaf))
	}
	// A pool without the internal CA must fail verification, not silently pass.
	if _, err := ServedSerial(context.Background(), addr, "nats", x509.NewCertPool()); err == nil {
		t.Fatal("verification passed without the issuing CA")
	}
}
