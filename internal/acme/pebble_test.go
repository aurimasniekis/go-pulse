//go:build acme

// Integration test against pebble + challtestsrv in Docker (scripts/test-acme.sh, `make test-acme`): obtain a
// certificate with DNS-01, install it into nats.conf next to the internal leaf, reload a real nats-server and verify
// both certificates are served by SNI (PL-EDGE-006, ADR-0023).
package acme

import (
	"context"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/go-acme/lego/v4/challenge/dns01"

	"github.com/aurimasniekis/go-pulse/internal/bootstrap"
	"github.com/aurimasniekis/go-pulse/internal/certs"
	"github.com/aurimasniekis/go-pulse/internal/sysapi"
	"github.com/aurimasniekis/go-pulse/internal/testutil/natstest"
)

const (
	pebbleImage = "ghcr.io/letsencrypt/pebble:2.10.1@sha256:ddf230642b1a584f519f32e347de1b05a6e4c1f6c35c1863b33effeab5f78199"
	challImage  = "ghcr.io/letsencrypt/pebble-challtestsrv:2.10.1@sha256:12ce21884def456bcf9786542113949e1f19dc7738d2c70e156c2d0c38a1405b"
)

func docker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.CommandContext(context.Background(), "docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func hostPort(t *testing.T, ctr, port string) string {
	t.Helper()
	for _, l := range strings.Split(docker(t, "port", ctr, port), "\n") {
		if strings.HasPrefix(l, "127.0.0.1:") {
			return l
		}
	}
	t.Fatalf("no 127.0.0.1 mapping for %s %s", ctr, port)
	return ""
}

func waitHTTP(t *testing.T, c *http.Client, url string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
		if resp, err := c.Do(req); err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("%s did not come up", url)
}

func startPebble(t *testing.T) (dir, mgmt, dns string, ca []byte) {
	t.Helper()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	network, chall, pebble := "pulse-acme-"+suffix, "pulse-chall-"+suffix, "pulse-pebble-"+suffix
	docker(t, "network", "create", network)
	t.Cleanup(func() {
		_ = exec.CommandContext(context.Background(), "docker", "rm", "-f", pebble, chall).Run()
		_ = exec.CommandContext(context.Background(), "docker", "network", "rm", network).Run()
	})
	docker(t, "run", "-d", "--name", chall, "--network", network, "--network-alias", "challtestsrv",
		"-p", "127.0.0.1::8055", "-p", "127.0.0.1::8053/udp", challImage,
		"-defaultIPv6", "", "-defaultIPv4", "127.0.0.1", "-http01", "", "-https01", "", "-tlsalpn01", "", "-doh", "")
	docker(t, "run", "-d", "--name", pebble, "--network", network, "-e", "PEBBLE_VA_NOSLEEP=1", "-e", "PEBBLE_WFE_NONCEREJECT=0",
		"-p", "127.0.0.1::14000", pebbleImage, "-config", "/test/config/pebble-config.json", "-dnsserver", "challtestsrv:8053")
	ca, err := os.ReadFile("../../deploy/dev/pebble-minica.pem")
	if err != nil {
		t.Fatal(err)
	}
	dir = "https://" + hostPort(t, pebble, "14000/tcp") + "/dir"
	mgmt = "http://" + hostPort(t, chall, "8055/tcp")
	dns = strings.TrimPrefix(docker(t, "port", chall, "8053/udp"), "0.0.0.0:")
	waitHTTP(t, &http.Client{Timeout: 2 * time.Second}, mgmt)
	hc, err := DirectoryClient(ca)
	if err != nil {
		t.Fatal(err)
	}
	waitHTTP(t, hc, dir)
	return dir, mgmt, firstLine(dns), ca
}

func firstLine(s string) string { return strings.SplitN(s, "\n", 2)[0] }

func TestPebbleObtainInstallReload(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	dir, mgmt, dnsAddr, ca := startPebble(t)
	env := natstest.Start(t, natstest.Options{NoWebsocket: true, Process: true})
	sys := sysapi.New(env.SysConn())
	addr := strings.TrimPrefix(env.ClientURL, "tls://")

	m, err := New(Options{
		Directory: dir, Domain: natstest.RawHost, AccountKey: mustKey(t, env.Keys.ACMEAccountKey),
		Provider:    &ChallTestSrv{URL: mgmt, HTTP: &http.Client{Timeout: 5 * time.Second}},
		DNSOpts:     []dns01.ChallengeOption{dns01.AddRecursiveNameservers([]string{dnsAddr}), dns01.PropagationWait(0, true)},
		DirectoryCA: ca,
		CertFile:    env.Layout.ACMECert(), KeyFile: env.Layout.ACMEKey(),
		Install: func() error { _, err := bootstrap.Init(env.InitOptions); return err },
		Reload:  sys.Reload,
		Served: func(ctx context.Context, name string, roots *x509.CertPool) (string, error) {
			return ServedSerial(ctx, addr, name, roots)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := m.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	st := m.Status()
	if !st.Verified || st.Serial == "" || st.LastError != "" || m.Due() {
		t.Fatalf("status = %+v", st)
	}
	conf, _ := os.ReadFile(env.Layout.NATSConf())
	if strings.Index(string(conf), "acme.crt") > strings.Index(string(conf), "server.crt") {
		t.Fatalf("ACME pair must be listed first:\n%s", conf)
	}

	// pulsed's own connections keep verifying SNI "nats" against the internal CA.
	internal := x509.NewCertPool()
	internal.AppendCertsFromPEM([]byte(env.Keys.CACert))
	b, _ := os.ReadFile(env.Layout.ServerCert())
	leaf, _ := certs.ParseLeaf(b)
	if got, err := ServedSerial(ctx, addr, "nats", internal); err != nil || got != certs.SerialHex(leaf) {
		t.Fatalf("SNI nats: serial %q err %v, want internal leaf %s", got, err, certs.SerialHex(leaf))
	}
	if nc := env.SysConn(); !nc.IsConnected() {
		t.Fatal("SYS connection lost after reload")
	}

	// A fresh certificate is not renewed again.
	if m.Due() {
		t.Fatal("certificate still due after issue")
	}
	if err := m.Ensure(ctx); err != nil || m.Status().Serial != st.Serial {
		t.Fatalf("second Ensure re-issued: err %v serial %s", err, m.Status().Serial)
	}

	// A restarted manager resolves the existing account by key and renews on demand.
	_ = os.Remove(env.Layout.ACMECert())
	if err := m.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	if m.Status().Serial == st.Serial || !m.Status().Verified {
		t.Fatalf("renewal did not install a new certificate: %+v", m.Status())
	}
}

func mustKey(t *testing.T, p string) any {
	t.Helper()
	k, err := ParseAccountKey(p)
	if err != nil {
		t.Fatal(err)
	}
	return k
}
