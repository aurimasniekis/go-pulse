package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	if _, err := Load(env(map[string]string{"PULSE_MASTER_KEY_FILE": keyFile(t)})); err == nil {
		t.Fatal("prod accepted a plain-HTTP public listener")
	}
	c, err := Load(env(map[string]string{"PULSE_MASTER_KEY_FILE": keyFile(t), "PULSE_PUBLIC_ADDR": "off"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.PublicAddr != "" || c.Profile != ProfileProd || c.AdminAddr != "127.0.0.1:8080" || c.MetricsAddr != "127.0.0.1:9464" || len(c.MasterKey) != 32 {
		t.Fatalf("%+v", c)
	}
}

func TestProdRefusals(t *testing.T) {
	kf := keyFile(t)
	cases := map[string]map[string]string{
		"plain http public listener":        {"PULSE_MASTER_KEY_FILE": kf},
		"cert without key":                  {"PULSE_MASTER_KEY_FILE": kf, "PULSE_ORIGIN_CERT_FILE": "/x"},
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
		"PULSE_PUBLIC_ADDR": "off", "PULSE_ADMIN_HOSTS": "Pulse.local, x",
	}))
	if err != nil || c.AdminAddr != "0.0.0.0:8080" || len(c.AdminHosts) != 2 || c.AdminHosts[0] != "pulse.local" {
		t.Fatalf("%v %v", err, c.AdminHosts)
	}
}

func TestDevAllowsPlainKey(t *testing.T) {
	c, err := Load(env(map[string]string{"PULSE_PROFILE": "dev", "PULSE_MASTER_KEY": secretbox.GenerateMasterKey(), "PULSE_ADMIN_ADDR": "0.0.0.0:8080"}))
	if err != nil || !c.Dev() {
		t.Fatalf("%v", err)
	}
}

func TestProdPublicWithTLSAndAOP(t *testing.T) {
	c, err := Load(env(map[string]string{"PULSE_MASTER_KEY_FILE": keyFile(t), "PULSE_ORIGIN_CERT_FILE": "/c", "PULSE_ORIGIN_KEY_FILE": "/k",
		"PULSE_AOP_CA_FILE": "/aop", "PULSE_BAN_ALLOW": "203.0.113.0/24"}))
	if err != nil || c.BanThreshold != 5 || c.BanDuration != time.Hour || len(c.BanAllow) != 1 || !c.CFRefresh {
		t.Fatalf("%+v %v", c, err)
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

func TestACME(t *testing.T) {
	kf := keyFile(t)
	tok := filepath.Join(t.TempDir(), "cf")
	if err := os.WriteFile(tok, []byte("cf-dns-token\n"), 0o600); err != nil { // gitleaks:allow (test literal)
		t.Fatal(err)
	}
	base := func(kv ...string) map[string]string {
		m := map[string]string{"PULSE_MASTER_KEY_FILE": kf, "PULSE_PUBLIC_ADDR": "off", "PULSE_ACME_ENABLED": "true", "PULSE_RAW_HOST": "r.example.test"}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	c, err := Load(env(base("PULSE_ACME_CF_TOKEN_FILE", tok)))
	if err != nil {
		t.Fatal(err)
	}
	if c.ACMECFToken != "cf-dns-token" || c.ACMEDNSProvider != "cloudflare" || !strings.Contains(c.ACMEDirectory, "letsencrypt.org") {
		t.Fatalf("%+v", c)
	}
	refused := map[string]map[string]string{
		"cloudflare without token": base(),
		"plain token in prod":      base("PULSE_ACME_CF_TOKEN", "x"),
		"raw host missing":         base("PULSE_ACME_CF_TOKEN_FILE", tok, "PULSE_RAW_HOST", ""),
		"raw host localhost":       base("PULSE_ACME_CF_TOKEN_FILE", tok, "PULSE_RAW_HOST", "localhost"),
		"challtestsrv in prod":     base("PULSE_ACME_DNS_PROVIDER", "challtestsrv", "PULSE_ACME_CHALLTESTSRV_URL", "http://c:8055"),
		"challtestsrv without url": base("PULSE_PROFILE", "dev", "PULSE_ACME_DNS_PROVIDER", "challtestsrv"),
		"unknown provider":         base("PULSE_PROFILE", "dev", "PULSE_ACME_DNS_PROVIDER", "route53"),
	}
	for name, m := range refused {
		if _, err := Load(env(m)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := Load(env(base("PULSE_PROFILE", "dev", "PULSE_ACME_DNS_PROVIDER", "challtestsrv", "PULSE_ACME_CHALLTESTSRV_URL", "http://c:8055"))); err != nil {
		t.Fatalf("dev challtestsrv refused: %v", err)
	}
}

func TestNotifiers(t *testing.T) {
	kf := keyFile(t)
	tok := filepath.Join(t.TempDir(), "tg")
	if err := os.WriteFile(tok, []byte("123:abc\n"), 0o600); err != nil { // gitleaks:allow (test literal)
		t.Fatal(err)
	}
	c, err := Load(env(map[string]string{"PULSE_MASTER_KEY_FILE": kf, "PULSE_PUBLIC_ADDR": "off", "PULSE_TELEGRAM_BOT_TOKEN_FILE": tok,
		"PULSE_TELEGRAM_CHAT_ID": "-100", "PULSE_ALERT_REPEAT": "off"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.TelegramToken != "123:abc" || c.AlertRepeat >= 0 || c.AlertInterval != time.Minute || c.TelegramMinSeverity != "warning" {
		t.Fatalf("%+v", c)
	}
	for name, m := range map[string]map[string]string{
		"token without chat":    {"PULSE_TELEGRAM_BOT_TOKEN_FILE": tok},
		"plain token in prod":   {"PULSE_TELEGRAM_BOT_TOKEN": "x", "PULSE_TELEGRAM_CHAT_ID": "1"},
		"bad severity":          {"PULSE_DISCORD_MIN_SEVERITY": "loud"},
		"interval too short":    {"PULSE_ALERT_INTERVAL": "1s"},
		"discord url not https": {"PULSE_DISCORD_WEBHOOK_URL_FILE": tok},
	} {
		m["PULSE_MASTER_KEY_FILE"], m["PULSE_PUBLIC_ADDR"] = kf, "off"
		if _, err := Load(env(m)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestBackups(t *testing.T) {
	kf := keyFile(t)
	dir := t.TempDir()
	ak, sk := filepath.Join(dir, "ak"), filepath.Join(dir, "sk")
	_ = os.WriteFile(ak, []byte("AKID\n"), 0o600)
	_ = os.WriteFile(sk, []byte("secret\n"), 0o600) // gitleaks:allow (test literal)
	base := func(kv ...string) map[string]string {
		m := map[string]string{"PULSE_MASTER_KEY_FILE": kf, "PULSE_PUBLIC_ADDR": "off", "PULSE_BACKUP_ENABLED": "true",
			"PULSE_BACKUP_S3_ENDPOINT": "s3.example.test", "PULSE_BACKUP_S3_BUCKET": "pulse", "PULSE_BACKUP_S3_ACCESS_KEY_FILE": ak,
			"PULSE_BACKUP_S3_SECRET_KEY_FILE": sk, "PULSE_BACKUP_AGE_RECIPIENTS": "age1example"}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	c, err := Load(env(base()))
	if err != nil {
		t.Fatal(err)
	}
	if c.BackupS3AccessKey != "AKID" || c.BackupAt != "03:30" || c.BackupKeepDaily != 7 || c.BackupS3Prefix != "pulse/" {
		t.Fatalf("%+v", c)
	}
	for name, m := range map[string]map[string]string{
		"no recipients":      base("PULSE_BACKUP_AGE_RECIPIENTS", ""),
		"no bucket":          base("PULSE_BACKUP_S3_BUCKET", ""),
		"bad time":           base("PULSE_BACKUP_AT", "25:99"),
		"insecure in prod":   base("PULSE_BACKUP_S3_INSECURE", "true"),
		"bad lookup":         base("PULSE_BACKUP_S3_LOOKUP", "magic"),
		"no daily retention": base("PULSE_BACKUP_KEEP_DAILY", "0"),
	} {
		if _, err := Load(env(m)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
