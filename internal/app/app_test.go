package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

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
