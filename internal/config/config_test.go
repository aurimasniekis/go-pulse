package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aurimasniekis/go-pulse/internal/secretbox"
)

func env(m map[string]string) Getenv { return func(k string) string { return m[k] } }

func keyFile(t *testing.T) string {
	p := filepath.Join(t.TempDir(), "mk")
	if err := os.WriteFile(p, []byte(secretbox.GenerateMasterKey()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestProdDefaults(t *testing.T) {
	c, err := Load(env(map[string]string{"PULSE_MASTER_KEY_FILE": keyFile(t)}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Profile != ProfileProd || c.AdminAddr != "127.0.0.1:8080" || c.MetricsAddr != "127.0.0.1:9464" || len(c.MasterKey) != 32 {
		t.Fatalf("%+v", c)
	}
}

func TestProdRefusals(t *testing.T) {
	kf := keyFile(t)
	cases := map[string]map[string]string{
		"admin public with auth none":       {"PULSE_MASTER_KEY_FILE": kf, "PULSE_ADMIN_PUBLIC": "true"},
		"non-loopback admin with auth none": {"PULSE_MASTER_KEY_FILE": kf, "PULSE_ADMIN_ADDR": "0.0.0.0:8080"},
		"plain master key in prod":          {"PULSE_MASTER_KEY": secretbox.GenerateMasterKey()},
		"missing master key":                {},
		"oidc not implemented":              {"PULSE_MASTER_KEY_FILE": kf, "PULSE_AUTH_MODE": "oidc"},
		"bad profile":                       {"PULSE_MASTER_KEY_FILE": kf, "PULSE_PROFILE": "staging"},
		"bad size":                          {"PULSE_MASTER_KEY_FILE": kf, "PULSE_EVENTS_MAX_BYTES": "lots"},
	}
	for name, m := range cases {
		if _, err := Load(env(m)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestContainerAdminPublishedOnLoopback(t *testing.T) {
	c, err := Load(env(map[string]string{
		"PULSE_MASTER_KEY_FILE": keyFile(t), "PULSE_ADMIN_ADDR": "0.0.0.0:8080", "PULSE_ADMIN_PUBLISHED_LOOPBACK": "true",
	}))
	if err != nil || c.AdminAddr != "0.0.0.0:8080" {
		t.Fatalf("%v", err)
	}
}

func TestDevAllowsPlainKey(t *testing.T) {
	c, err := Load(env(map[string]string{"PULSE_PROFILE": "dev", "PULSE_MASTER_KEY": secretbox.GenerateMasterKey(), "PULSE_ADMIN_ADDR": "0.0.0.0:8080"}))
	if err != nil || !c.Dev() {
		t.Fatalf("%v", err)
	}
}

func TestParseBytes(t *testing.T) {
	for in, want := range map[string]int64{"1024": 1024, "10GiB": 10 << 30, "60G": 60e9, "1.5MiB": 1572864, "512 MiB": 512 << 20} {
		got, err := ParseBytes(in)
		if err != nil || got != want {
			t.Errorf("%s: %d %v", in, got, err)
		}
	}
	for _, in := range []string{"", "x", "-1", "GiB"} {
		if _, err := ParseBytes(in); err == nil || !strings.Contains(err.Error(), "invalid") {
			t.Errorf("%q accepted", in)
		}
	}
}
