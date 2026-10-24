package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseGrant(t *testing.T) {
	g, err := parseGrant("durable:evt.home.>:indexer")
	if err != nil || g != (grant{Action: "durable", Subject: "evt.home.>", Name: "indexer"}) || g.String() != "durable:evt.home.>:indexer" {
		t.Fatalf("%+v %v", g, err)
	}
	if g, err := parseGrant("blobs"); err != nil || g.Action != "blobs" {
		t.Fatalf("%+v %v", g, err)
	}
	if _, err := parseGrant("admin:evt.>"); err == nil {
		t.Fatal("bad action accepted")
	}
}

func TestClientsCreateAndAdminErrors(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/admin/v1/clients":
			if r.Header.Get("X-Pulse-CSRF") != "1" {
				http.Error(w, "csrf", http.StatusForbidden)
				return
			}
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"client":{"id":"c1","name":"bot"},"creds":"-----BEGIN NATS USER JWT-----\nX\n------END NATS USER JWT------\n"}`))
		case r.URL.Path == "/api/admin/v1/streams":
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"title":"Service Unavailable","detail":"NATS is down"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	t.Setenv("PULSE_CONFIG", filepath.Join(t.TempDir(), "cfg.json"))
	out := filepath.Join(t.TempDir(), "bot.creds")
	run := func(args ...string) (string, error) {
		cmd := newRootCmd()
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		cmd.SetErr(&buf)
		cmd.SetArgs(args)
		err := cmd.Execute()
		return buf.String(), err
	}
	if _, err := run("clients", "create", "bot", "--admin-url", srv.URL, "--grant", "publish:evt.home.>", "--grant", "blobs", "--out", out); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(out)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("creds file %v %v", st, err)
	}
	gs := gotBody["grants"].([]any)
	if gotBody["name"] != "bot" || len(gs) != 2 || gs[0].(map[string]any)["subject"] != "evt.home.>" {
		t.Fatalf("%v", gotBody)
	}
	if _, err := run("clients", "create", "bot", "--admin-url", srv.URL, "--out", out); err == nil || !strings.Contains(err.Error(), "exists") {
		t.Fatalf("overwrote credentials: %v", err)
	}
	if _, err := run("streams", "--admin-url", srv.URL); err == nil || !strings.Contains(err.Error(), "NATS is down") {
		t.Fatalf("problem detail not surfaced: %v", err)
	}
	if _, err := run("streams", "--admin-url", "http://127.0.0.1:1"); err == nil || !strings.Contains(err.Error(), "ssh tunnel") {
		t.Fatalf("connection error not explained: %v", err)
	}
}
