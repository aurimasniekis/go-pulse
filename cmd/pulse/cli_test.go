package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-pulse/internal/audit"
	"github.com/aurimasniekis/go-pulse/internal/clients"
	"github.com/aurimasniekis/go-pulse/internal/natsauth"
	"github.com/aurimasniekis/go-pulse/internal/store"
	"github.com/aurimasniekis/go-pulse/internal/streams"
	"github.com/aurimasniekis/go-pulse/internal/sysapi"
	"github.com/aurimasniekis/go-pulse/internal/testutil/natstest"
	"github.com/aurimasniekis/go-pulse/pkg/pulse"
)

func TestParseSince(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	cases := map[string]time.Time{
		"2026-01-01T00:00:00Z": time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		"24h":                  now.Add(-24 * time.Hour),
		"7d":                   now.Add(-7 * 24 * time.Hour),
		"2w":                   now.Add(-14 * 24 * time.Hour),
		"90m":                  now.Add(-90 * time.Minute),
	}
	for in, want := range cases {
		got, err := parseSince(in, now)
		if err != nil || !got.Equal(want) {
			t.Errorf("%s: %v %v", in, got, err)
		}
	}
	if got, err := parseSince("2026-01-01", now); err != nil || got.Year() != 2026 || got.Month() != 1 {
		t.Errorf("date: %v %v", got, err)
	}
	for _, bad := range []string{"", "yesterday", "-5h", "5x"} {
		if _, err := parseSince(bad, now); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func run(t *testing.T, args ...string) string {
	t.Helper()
	cmd := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := cmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("pulse %s: %v\n%s", strings.Join(args, " "), err, out.String())
	}
	return out.String()
}

// The manual flow from the plan: create a client, add a context, pub, replay with sub --since.
func TestCLIPubSubFlow(t *testing.T) {
	env := natstest.Start(t)
	ctx := context.Background()
	db, err := store.Open(ctx, store.Options{Path: env.Layout.DatabaseFile()})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc := clients.New(db, env.Keys, sysapi.New(env.SysConn()), nil)
	if _, err := svc.Sync(ctx, false); err != nil {
		t.Fatal(err)
	}
	app, _ := pulse.New(env.AppConn(), "test")
	if _, err := streams.EnsureEvents(ctx, app.JetStream(), 256<<20, false, nil); err != nil {
		t.Fatal(err)
	}
	_, creds, err := svc.Create(ctx, clients.CreateInput{Name: "cli", Grants: []natsauth.Grant{
		{Action: natsauth.ActPublish, Subject: "evt.test.>"}, {Action: natsauth.ActSubscribe, Subject: "evt.test.>"},
	}}, audit.System)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	credsPath, caPath := filepath.Join(dir, "cli.creds"), filepath.Join(dir, "ca.crt")
	_ = os.WriteFile(credsPath, creds, 0o600)
	_ = os.WriteFile(caPath, []byte(env.Keys.CACert), 0o600)
	t.Setenv("PULSE_CONFIG", filepath.Join(dir, "config.json"))

	run(t, "context", "add", "local", "--url", env.ClientURL, "--creds", credsPath, "--ca", caPath)
	if out := run(t, "context", "ls"); !strings.Contains(out, "* ") || !strings.Contains(out, "local") {
		t.Fatalf("context ls: %s", out)
	}
	out := run(t, "pub", "evt.test.thing.created", `{"n":1}`, "-t", "test.thing.created.v1")
	if !strings.Contains(out, "seq=1") {
		t.Fatalf("pub: %s", out)
	}
	out = run(t, "sub", "evt.test.>", "--since", "1h", "-n", "1", "--json")
	var e jsonEvent
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &e); err != nil {
		t.Fatalf("sub output %q: %v", out, err)
	}
	if e.Seq != 1 || e.Type != "test.thing.created.v1" || string(e.Data) != `{"n":1}` || e.Source != "pulse-cli" {
		t.Fatalf("%+v", e)
	}
	if out := run(t, "sub", "evt.test.>", "--seq", "1", "-n", "1"); !strings.HasPrefix(out, "1 ") {
		t.Fatalf("plain sub: %q", out)
	}
}
