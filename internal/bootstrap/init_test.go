package bootstrap

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aurimasniekis/go-pulse/internal/certs"
	"github.com/aurimasniekis/go-pulse/internal/secretbox"
)

func testOpts(t *testing.T) Options {
	t.Helper()
	master, _ := secretbox.ParseMasterKey(secretbox.GenerateMasterKey())
	root := t.TempDir()
	return Options{Layout: Layout{DataDir: root + "/pulsed", NATSDir: root + "/nats"}, MasterKey: master, RawHost: "r.example.test"}
}

func TestInitIdempotent(t *testing.T) {
	o := testOpts(t)
	r1, err := Init(o)
	if err != nil {
		t.Fatal(err)
	}
	if len(r1.Changed) == 0 {
		t.Fatal("first init changed nothing")
	}
	ks1, _ := LoadKeySet(o.Layout.KeysFile(), o.MasterKey)
	conf1, _ := os.ReadFile(o.Layout.NATSConf())
	r2, err := Init(o)
	if err != nil {
		t.Fatal(err)
	}
	if len(r2.Changed) != 0 {
		t.Fatalf("second init changed %v", r2.Changed)
	}
	ks2, _ := LoadKeySet(o.Layout.KeysFile(), o.MasterKey)
	conf2, _ := os.ReadFile(o.Layout.NATSConf())
	if ks1.Operator != ks2.Operator || ks1.AppAccount != ks2.AppAccount || string(conf1) != string(conf2) {
		t.Fatal("keys or nats.conf changed on re-run")
	}
	for _, s := range []string{"type: full", "resolver_preload", ks1.Pub(ks1.SysAccount), "max_payload: 1048576", `ping_interval: "30s"`} {
		if !strings.Contains(string(conf1), s) {
			t.Errorf("nats.conf lacks %q", s)
		}
	}
	for path, want := range map[string]os.FileMode{
		o.Layout.KeysFile(): 0o600, o.Layout.ServerKey(): 0o600, o.Layout.BanagentTokenFile(): 0o600, o.Layout.DataDir: 0o700,
	} {
		st, err := os.Stat(path)
		if err != nil || st.Mode().Perm() != want {
			t.Errorf("%s mode %v want %v (%v)", path, st.Mode().Perm(), want, err)
		}
	}
}

func TestInitWrongMasterKeyFailsClosed(t *testing.T) {
	o := testOpts(t)
	if _, err := Init(o); err != nil {
		t.Fatal(err)
	}
	o.MasterKey, _ = secretbox.ParseMasterKey(secretbox.GenerateMasterKey())
	if _, err := Init(o); !errors.Is(err, secretbox.ErrBadKey) {
		t.Fatalf("want ErrBadKey, got %v", err)
	}
}

func TestBootstrapLeafRenewal(t *testing.T) {
	o := testOpts(t)
	if _, err := Init(o); err != nil {
		t.Fatal(err)
	}
	read := func() string {
		b, _ := os.ReadFile(o.Layout.ServerCert())
		c, err := certs.ParseLeaf(b)
		if err != nil {
			t.Fatal(err)
		}
		return certs.SerialHex(c)
	}
	first := read()
	// 61 days later the 90-day leaf has < 30 days left and is renewed.
	o.Now = func() time.Time { return time.Now().Add(61 * 24 * time.Hour) }
	if _, err := Init(o); err != nil {
		t.Fatal(err)
	}
	if read() == first {
		t.Fatal("leaf not renewed near expiry")
	}
	// A new SAN forces a reissue.
	second := read()
	o.ExtraSANs = []string{"extra.example.test"}
	if _, err := Init(o); err != nil {
		t.Fatal(err)
	}
	if read() == second {
		t.Fatal("leaf not reissued for new SAN")
	}
	// A certificate from another issuer (ACME) is left alone.
	other, _ := certs.NewCA("le-stand-in", time.Now())
	leaf, _ := other.IssueServer(o.LeafNames(), time.Hour, time.Now())
	_ = os.WriteFile(o.Layout.ServerCert(), leaf.CertPEM, 0o644)
	_ = os.WriteFile(o.Layout.ServerKey(), leaf.KeyPEM, 0o600)
	if _, err := Init(o); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(o.Layout.ServerCert())
	if string(b) != string(leaf.CertPEM) {
		t.Fatal("init replaced a non-internal (ACME) certificate")
	}
}
