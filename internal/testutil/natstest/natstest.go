// Package natstest boots an embedded nats-server from the nats.conf that `pulsed init` generates (PL-TST-001).
package natstest

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"

	"github.com/aurimasniekis/go-pulse/internal/bootstrap"
	"github.com/aurimasniekis/go-pulse/internal/certs"
	"github.com/aurimasniekis/go-pulse/internal/natsconn"
	"github.com/aurimasniekis/go-pulse/internal/secretbox"
)

// Env is a bootstrapped data layout plus a running server.
type Env struct {
	T         testing.TB
	Layout    bootstrap.Layout
	MasterKey []byte
	Keys      *bootstrap.KeySet
	Server    *server.Server // nil when running as a subprocess
	ClientURL string
	proc      *exec.Cmd
	process   bool
	WSPort    int
	port      int
	wsPort    int
	monPort   int
}

// FreePort returns an unused TCP port on 127.0.0.1.
func FreePort(t testing.TB) int {
	t.Helper()
	var lc net.ListenConfig
	l, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// Options tweak Start.
type Options struct {
	NoWebsocket bool
	// Process runs a real nats-server binary (built from the pinned module) instead of the embedded server.
	// Use it for config RELOAD: the embedded server races internally during reload under -race (TEST_STRATEGY.md §3).
	Process bool
}

// Start runs bootstrap.Init into temp dirs and boots the server from the generated nats.conf.
func Start(t testing.TB, opts ...Options) *Env {
	t.Helper()
	var o Options
	if len(opts) > 0 {
		o = opts[0]
	}
	root := t.TempDir()
	master, err := secretbox.ParseMasterKey(secretbox.GenerateMasterKey())
	if err != nil {
		t.Fatal(err)
	}
	e := &Env{
		T:         t,
		Layout:    bootstrap.Layout{DataDir: root + "/pulsed", NATSDir: root + "/nats"},
		MasterKey: master,
		port:      FreePort(t),
		monPort:   FreePort(t),
		process:   o.Process,
	}
	ws := ""
	if !o.NoWebsocket {
		e.wsPort = FreePort(t)
		e.WSPort = e.wsPort
		ws = fmt.Sprintf("127.0.0.1:%d", e.wsPort)
	}
	if _, err := bootstrap.Init(bootstrap.Options{
		Layout:          e.Layout,
		MasterKey:       master,
		RawHost:         "r.pulse.test",
		ClientListen:    fmt.Sprintf("127.0.0.1:%d", e.port),
		MonitorListen:   fmt.Sprintf("127.0.0.1:%d", e.monPort),
		WebsocketListen: ws,
	}); err != nil {
		t.Fatal(err)
	}
	e.Keys, err = bootstrap.LoadKeySet(e.Layout.KeysFile(), master)
	if err != nil {
		t.Fatal(err)
	}
	e.ClientURL = fmt.Sprintf("tls://127.0.0.1:%d", e.port)
	e.boot()
	t.Cleanup(e.Shutdown)
	return e
}

func (e *Env) boot() {
	e.T.Helper()
	if e.process {
		e.bootProcess()
		return
	}
	opts, err := server.ProcessConfigFile(e.Layout.NATSConf())
	if err != nil {
		e.T.Fatalf("process generated nats.conf: %v", err)
	}
	opts.NoSigs = true
	opts.NoLog = os.Getenv("NATSTEST_LOG") == ""
	opts.Debug = os.Getenv("NATSTEST_LOG") == "debug"
	s, err := server.NewServer(opts)
	if err != nil {
		e.T.Fatalf("new server: %v", err)
	}
	if !opts.NoLog {
		s.ConfigureLogger()
	}
	go s.Start()
	if !s.ReadyForConnections(10 * time.Second) {
		e.T.Fatal("nats server not ready")
	}
	e.Server = s
}

var (
	buildOnce sync.Once
	binPath   string
	buildErr  error
)

// serverBinary builds nats-server at the version pinned in go.mod (cached by the Go build cache).
func serverBinary(t testing.TB) string {
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "natstest-bin-")
		if err != nil {
			buildErr = err
			return
		}
		binPath = filepath.Join(dir, "nats-server")
		out, err := exec.CommandContext(context.Background(), "go", "build", "-o", binPath, "github.com/nats-io/nats-server/v2").CombinedOutput() //nolint:gosec // fixed args, temp path
		if err != nil {
			buildErr = fmt.Errorf("build nats-server: %w: %s", err, out)
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return binPath
}

func (e *Env) bootProcess() {
	e.T.Helper()
	cmd := exec.CommandContext(context.Background(), serverBinary(e.T), "-c", e.Layout.NATSConf()) //nolint:gosec // test binary
	if os.Getenv("NATSTEST_LOG") != "" {
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	}
	if err := cmd.Start(); err != nil {
		e.T.Fatal(err)
	}
	e.proc = cmd
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		d := net.Dialer{Timeout: 200 * time.Millisecond}
		if c, err := d.DialContext(context.Background(), "tcp", fmt.Sprintf("127.0.0.1:%d", e.port)); err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.T.Fatal("nats-server process not ready")
}

// Shutdown stops the server.
func (e *Env) Shutdown() {
	if e.proc != nil {
		_ = e.proc.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _ = e.proc.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = e.proc.Process.Kill()
			<-done
		}
		e.proc = nil
	}
	if e.Server != nil {
		e.Server.Shutdown()
		e.Server.WaitForShutdown()
		e.Server = nil
	}
}

// Restart stops and restarts the server with the same config and store (simulates a container restart).
func (e *Env) Restart() {
	e.T.Helper()
	e.Shutdown()
	e.boot()
}

// ClientTLS trusts the internal CA and verifies the "nats" name (as pulsed does in compose).
func (e *Env) ClientTLS() *tls.Config {
	e.T.Helper()
	c, err := certs.ClientTLS("localhost", []byte(e.Keys.CACert))
	if err != nil {
		e.T.Fatal(err)
	}
	return c
}

// SysConn connects as pulsed's SYS user.
func (e *Env) SysConn() *nats.Conn {
	e.T.Helper()
	tok, seed, err := e.Keys.SysUser()
	if err != nil {
		e.T.Fatal(err)
	}
	return e.connect(tok, seed, "pulsed-sys-test")
}

// AppConn connects as pulsed's APP user. The APP account must have been pushed first.
func (e *Env) AppConn() *nats.Conn {
	e.T.Helper()
	tok, seed, err := e.Keys.AppUser()
	if err != nil {
		e.T.Fatal(err)
	}
	return e.connect(tok, seed, "pulsed-app-test")
}

func (e *Env) connect(tok string, seed []byte, name string) *nats.Conn {
	e.T.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	nc, err := natsconn.Connect(ctx, natsconn.Options{URL: e.ClientURL, TLS: e.ClientTLS(), JWT: tok, Seed: seed, Name: name})
	if err != nil {
		e.T.Fatal(err)
	}
	e.T.Cleanup(nc.Close)
	return nc
}

// ConnectCreds connects with a .creds blob (as a tool would) and returns the error instead of failing.
func (e *Env) ConnectCreds(creds []byte, opts ...nats.Option) (*nats.Conn, error) {
	path := e.T.TempDir() + "/client.creds"
	if err := os.WriteFile(path, creds, 0o600); err != nil {
		return nil, err
	}
	all := append([]nats.Option{
		nats.UserCredentials(path),
		nats.Secure(e.ClientTLS()),
		nats.NoReconnect(),
		nats.Timeout(3 * time.Second),
	}, opts...)
	return nats.Connect(e.ClientURL, all...)
}

// IsAuthErr reports whether err is a NATS authorization failure.
func IsAuthErr(err error) bool {
	return err != nil && (strings.Contains(strings.ToLower(err.Error()), "authorization") ||
		strings.Contains(strings.ToLower(err.Error()), "authentication"))
}
