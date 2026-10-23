package certs

import (
	"crypto/x509"
	"testing"
	"time"
)

func TestCAIssueAndVerify(t *testing.T) {
	now := time.Now()
	ca, err := NewCA("pulse internal CA", now)
	if err != nil {
		t.Fatal(err)
	}
	again, err := LoadCA(ca.CertPEM, ca.KeyPEM)
	if err != nil || !again.Cert.Equal(ca.Cert) {
		t.Fatalf("reload: %v", err)
	}
	leaf, err := again.IssueServer([]string{"nats", "localhost", "127.0.0.1", "r.example.test"}, 90*24*time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseLeaf(leaf.CertPEM)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert)
	for _, name := range []string{"nats", "r.example.test", "127.0.0.1"} {
		if _, err := parsed.Verify(x509.VerifyOptions{DNSName: name, Roots: pool}); err != nil {
			t.Fatalf("verify %s: %v", name, err)
		}
	}
	if !ca.IssuedBy(parsed) {
		t.Fatal("IssuedBy false for own leaf")
	}
	other, _ := NewCA("other", now)
	if other.IssuedBy(parsed) {
		t.Fatal("IssuedBy true for foreign CA")
	}
	if SerialHex(parsed) == "" || len(Fingerprint(parsed)) != 64 {
		t.Fatal("serial/fingerprint")
	}
}
