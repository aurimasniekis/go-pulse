package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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
