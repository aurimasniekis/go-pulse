package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-pulse/internal/backup"
	"github.com/aurimasniekis/go-pulse/internal/secretbox"
	"github.com/aurimasniekis/go-pulse/internal/store"
)

func TestVersionJSON(t *testing.T) {
	cmd := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"version", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var v map[string]string
	if err := json.Unmarshal(out.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v["version"] == "" || v["go"] == "" {
		t.Fatalf("missing fields: %v", v)
	}
}

func TestProbe(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
	defer srv.Close()
	if err := probe(context.Background(), srv.URL, time.Second); err != nil {
		t.Fatalf("ready: %v", err)
	}
	status = http.StatusServiceUnavailable
	if err := probe(context.Background(), srv.URL, time.Second); err == nil {
		t.Fatal("expected error when not ready")
	}
}

func TestInitAndKeysExport(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PULSE_PROFILE", "dev")
	t.Setenv("PULSE_MASTER_KEY", secretbox.GenerateMasterKey())
	t.Setenv("PULSE_DATA_DIR", dir+"/pulsed")
	t.Setenv("PULSE_NATS_DIR", dir+"/nats")
	run := func(args ...string) (string, error) {
		cmd := newRootCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		err := cmd.ExecuteContext(context.Background())
		return out.String(), err
	}
	if _, err := run("init"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("keys", "export"); err == nil {
		t.Fatal("export without confirmation succeeded")
	}
	out, err := run("keys", "export", "--yes-i-understand")
	if err != nil {
		t.Fatal(err)
	}
	var ks map[string]any
	if err := json.Unmarshal([]byte(out), &ks); err != nil || ks["operator"] == "" {
		t.Fatalf("export output: %v", err)
	}
	db, err := store.Open(context.Background(), store.Options{Path: dir + "/pulsed/pulse.db"})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	_ = db.R().QueryRow("SELECT COUNT(*) FROM audit WHERE action = 'keys.export'").Scan(&n)
	if n != 1 {
		t.Fatalf("audit records: %d", n)
	}
}

func TestBackupKeygen(t *testing.T) {
	cmd := newRootCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"backup", "keygen"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	ids, err := backup.ParseIdentities(out.String())
	if err != nil || len(ids) != 1 {
		t.Fatalf("identity file does not parse: %v\n%s", err, out.String())
	}
	rcpt := strings.TrimSpace(strings.TrimPrefix(errOut.String(), "recipient:"))
	if _, err := backup.ParseRecipients(rcpt); err != nil || !strings.Contains(out.String(), "# public key: "+rcpt) {
		t.Fatalf("recipient %q: %v", rcpt, err)
	}
}

// pulsed admin bootstrap issues a setup link while pulsed holds the database lock, and only until an administrator
// exists (ADR-0038).
func TestAdminBootstrap(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PULSE_PROFILE", "dev")
	t.Setenv("PULSE_MASTER_KEY", secretbox.GenerateMasterKey())
	t.Setenv("PULSE_DATA_DIR", dir+"/pulsed")
	t.Setenv("PULSE_NATS_DIR", dir+"/nats")
	t.Setenv("PULSE_ADMIN_URL", "http://127.0.0.1:18080")
	db, err := store.Open(context.Background(), store.Options{Path: dir + "/pulsed/pulse.db"})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close() // holds the lock, as pulsed serve does
	run := func(args ...string) (string, error) {
		cmd := newRootCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		err := cmd.Execute()
		return out.String(), err
	}
	out, err := run("admin", "bootstrap")
	if err != nil || !strings.Contains(out, "http://127.0.0.1:18080/setup?token=") {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, err := run("admin", "recover", "--email", "nobody@example.com"); err == nil {
		t.Fatal("recovered a user that does not exist")
	}
	if out, err := run("admin", "recover", "--email", "root@example.com", "--new-admin"); err != nil || !strings.Contains(out, "/recover?token=") {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, err := run("admin", "bootstrap"); err == nil {
		t.Fatal("setup link issued although an administrator exists")
	}
	if _, err := run("admin", "token", "--email", "ci@example.com", "--role", "operator"); err == nil {
		t.Fatal("token for a missing user without --create")
	}
	out, err = run("admin", "token", "--email", "ci@example.com", "--create", "--role", "operator")
	if err != nil || !strings.HasPrefix(out, "pulse_pat_") {
		t.Fatalf("%v\n%s", err, out)
	}
}
