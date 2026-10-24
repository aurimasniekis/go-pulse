package app

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nkeys"

	"github.com/aurimasniekis/go-pulse/internal/certs"
	"github.com/aurimasniekis/go-pulse/internal/natsauth"

	"github.com/aurimasniekis/go-pulse/internal/config"
	"github.com/aurimasniekis/go-pulse/internal/testutil/natstest"
)

func testConfig(t *testing.T, env *natstest.Env) *config.Config {
	t.Helper()
	return &config.Config{
		Profile:         config.ProfileDev,
		Layout:          env.Layout,
		MasterKey:       env.MasterKey,
		NATSURL:         env.ClientURL,
		NATSTLSName:     "localhost",
		MetricsAddr:     fmt.Sprintf("127.0.0.1:%d", natstest.FreePort(t)),
		AdminAddr:       fmt.Sprintf("127.0.0.1:%d", natstest.FreePort(t)),
		PublicAddr:      fmt.Sprintf("127.0.0.1:%d", natstest.FreePort(t)),
		AuthMode:        "none",
		EventsMaxBytes:  1 << 30,
		BanThreshold:    5,
		BanWindow:       10 * time.Minute,
		BanDuration:     time.Hour,
		BanMax:          720 * time.Hour,
		ShutdownTimeout: 5 * time.Second,
	}
}

func get(t *testing.T, url string) int {
	t.Helper()
	resp, err := http.Get(url) //nolint:gosec // test URL
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func getBody(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url) //nolint:gosec // test URL
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func TestStartReadyShutdown(t *testing.T) {
	env := natstest.Start(t)
	cfg := testConfig(t, env)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	a, err := Start(ctx, cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	if code := get(t, "http://"+cfg.MetricsAddr+"/readyz"); code != http.StatusOK {
		t.Fatalf("readyz %d", code)
	}
	if code := get(t, "http://"+cfg.MetricsAddr+"/healthz"); code != http.StatusOK {
		t.Fatalf("healthz %d", code)
	}
	// /metrics: pulsed counters, scrape-time NATS and stream gauges, Go runtime (PL-OBS-002).
	get(t, "http://"+cfg.AdminAddr+"/api/admin/v1/system/info")
	body := getBody(t, "http://"+cfg.MetricsAddr+"/metrics")
	for _, want := range []string{`pulse_build_info{version=`, "nats_up 1", `pulse_stream_bytes{stream="EVENTS"}`, "nats_jetstream_streams",
		`pulse_http_requests_total{code="200",listener="admin",method="GET",route="/api/admin/v1/system"}`, "pulse_bans_active 0",
		`pulse_alerts_firing{severity="critical"}`, `pulse_certificate_not_after_timestamp_seconds{certificate="Internal CA"}`, "go_goroutines"} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics lacks %s", want)
		}
	}
	if a.Alerts == nil {
		t.Fatal("alert engine not started")
	}
	if rules, err := a.Alerts.List(ctx); err != nil || len(rules) < 9 {
		t.Fatalf("builtin rules not seeded: %d %v", len(rules), err)
	}
	// A second pulsed on the same data directory is refused (PL-DB-003).
	if _, err := Start(ctx, testConfig(t, env), log); err == nil {
		t.Fatal("second pulsed started on the same data dir")
	}
	a.Shutdown(ctx)

	// Restart on the same data: no re-push needed, ready again.
	cfg2 := testConfig(t, env)
	b, err := Start(ctx, cfg2, log)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Shutdown(ctx)
	if pushed, err := b.Clients.Sync(ctx, false); err != nil || pushed {
		t.Fatalf("restart re-pushed the APP account (pushed=%v err=%v)", pushed, err)
	}
}

// Prod-style public listener: origin certificate + AOP. Only clients with a certificate from the AOP CA get through;
// NATS auth failures reach the ban detector.
func TestPublicTLSWithAOPAndAuthErrors(t *testing.T) {
	env := natstest.Start(t)
	cfg := testConfig(t, env)
	dir := t.TempDir()
	now := time.Now()
	originCA, _ := certs.NewCA("origin", now)
	origin, _ := originCA.IssueServer([]string{"events.example.test", "127.0.0.1"}, time.Hour, now)
	aopCA, _ := certs.NewCA("aop", now)
	cf, _ := aopCA.IssueClient("cloudflare", time.Hour, now)
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cfg.OriginCertFile, cfg.OriginKeyFile, cfg.AOPCAFile = write("o.crt", origin.CertPEM), write("o.key", origin.KeyPEM), write("aop.crt", aopCA.CertPEM)
	cfg.BanThreshold, cfg.BanWindow, cfg.BanDuration, cfg.BanMax = 5, 10*time.Minute, time.Hour, 720*time.Hour
	a, err := Start(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Shutdown(context.Background())

	roots := x509.NewCertPool()
	roots.AddCert(originCA.Cert)
	get := func(withCert bool) (int, error) {
		tc := &tls.Config{RootCAs: roots, ServerName: "events.example.test", MinVersion: tls.VersionTLS12}
		if withCert {
			pair, _ := tls.X509KeyPair(cf.CertPEM, cf.KeyPEM)
			tc.Certificates = []tls.Certificate{pair}
		}
		c := &http.Client{Transport: &http.Transport{TLSClientConfig: tc}, Timeout: 3 * time.Second}
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://"+cfg.PublicAddr+"/healthz", nil)
		resp, err := c.Do(req)
		if err != nil {
			return 0, err
		}
		resp.Body.Close()
		return resp.StatusCode, nil
	}
	if code, err := get(true); err != nil || code != http.StatusOK {
		t.Fatalf("with AOP certificate: %d %v", code, err)
	}
	if _, err := get(false); err == nil {
		t.Fatal("public listener accepted a connection without a client certificate")
	}

	// A NATS login with an unknown key produces an AUTH.ERR advisory that the ban detector sees (loopback is never banned,
	// so it is recorded as ignored).
	stranger, _ := nkeys.CreateAccount()
	user, _ := nkeys.CreateUser()
	upub, _ := user.PublicKey()
	tok, _ := natsauth.UserJWT(upub, env.Keys.Pub(env.Keys.AppAccount), stranger, natsauth.UserOptions{})
	seed, _ := user.Seed()
	creds, _ := natsauth.Creds(tok, seed)
	_, _ = env.ConnectCreds(creds)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if r := a.Bans.Recent(); len(r) > 0 && r[0].Source == "nats" && r[0].IP == "127.0.0.1" && r[0].Ignored != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("auth error not seen by the detector: %+v", a.Bans.Recent())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestConfigSummaryHidesSecrets(t *testing.T) {
	cfg := &config.Config{MasterKey: []byte("0123456789abcdef0123456789abcdef"), TelegramToken: "123:abc", BackupS3SecretKey: "s3cr3t"} // gitleaks:allow (test literal)
	b, _ := json.Marshal(configSummary(cfg))
	for _, secret := range []string{"123:abc", "s3cr3t", "0123456789abcdef"} {
		if strings.Contains(string(b), secret) {
			t.Fatalf("summary leaks %q", secret)
		}
	}
	if !strings.Contains(string(b), `"env":"PULSE_TELEGRAM_BOT_TOKEN_FILE","value":"set","secret":true`) {
		t.Fatalf("%s", b)
	}
}
