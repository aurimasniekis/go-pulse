// Package certs holds TLS material handling: the internal CA and bootstrap leaf for NATS (PL-EDGE-007),
// and later the origin certificate, AOP verification and ACME (M8, M9).
package certs

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"time"
)

// CA is the internal certificate authority.
type CA struct {
	Cert *x509.Certificate
	Key  *ecdsa.PrivateKey
	// CertPEM and KeyPEM are the encoded forms (the key is stored only inside the sealed key store).
	CertPEM []byte
	KeyPEM  []byte
}

func serial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
}

// NewCA creates a self-signed ECDSA P-256 CA valid for ten years.
func NewCA(name string, now time.Time) (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	sn, err := serial()
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          sn,
		Subject:               pkix.Name{CommonName: name, Organization: []string{"pulse"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	return caFromDER(der, key)
}

func caFromDER(der []byte, key *ecdsa.PrivateKey) (*CA, error) {
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	return &CA{
		Cert:    cert,
		Key:     key,
		CertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		KeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
	}, nil
}

// LoadCA parses a CA from PEM.
func LoadCA(certPEM, keyPEM []byte) (*CA, error) {
	cb, _ := pem.Decode(certPEM)
	kb, _ := pem.Decode(keyPEM)
	if cb == nil || kb == nil {
		return nil, errors.New("certs: invalid CA PEM")
	}
	key, err := x509.ParseECPrivateKey(kb.Bytes)
	if err != nil {
		return nil, err
	}
	return caFromDER(cb.Bytes, key)
}

// Leaf is an issued certificate with its key, PEM encoded.
type Leaf struct {
	CertPEM []byte // leaf followed by the issuing CA
	KeyPEM  []byte
	Cert    *x509.Certificate
}

// IssueServer issues an ECDSA P-256 server (and client) certificate for the given DNS names and IPs.
func (ca *CA) IssueServer(names []string, ttl time.Duration, now time.Time) (*Leaf, error) {
	return ca.issue(names, ttl, now, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth})
}

// IssueClient issues a client-auth-only certificate (used in tests for AOP).
func (ca *CA) IssueClient(cn string, ttl time.Duration, now time.Time) (*Leaf, error) {
	return ca.issue([]string{cn}, ttl, now, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})
}

func (ca *CA) issue(names []string, ttl time.Duration, now time.Time, eku []x509.ExtKeyUsage) (*Leaf, error) {
	if len(names) == 0 {
		return nil, errors.New("certs: at least one name is required")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	sn, err := serial()
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: sn,
		Subject:      pkix.Name{CommonName: names[0]},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(ttl),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  eku,
	}
	for _, n := range names {
		if ip := net.ParseIP(n); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, n)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, &key.PublicKey, ca.Key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	certPEM = append(certPEM, ca.CertPEM...)
	return &Leaf{CertPEM: certPEM, KeyPEM: pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), Cert: cert}, nil
}

// ParseLeaf returns the first certificate in a PEM bundle.
func ParseLeaf(certPEM []byte) (*x509.Certificate, error) {
	b, _ := pem.Decode(certPEM)
	if b == nil || b.Type != "CERTIFICATE" {
		return nil, errors.New("certs: no certificate in PEM")
	}
	return x509.ParseCertificate(b.Bytes)
}

// IssuedBy reports whether cert was signed by the CA.
func (ca *CA) IssuedBy(cert *x509.Certificate) bool {
	return cert.CheckSignatureFrom(ca.Cert) == nil
}

// SerialHex renders a certificate serial for logs and comparisons.
func SerialHex(c *x509.Certificate) string { return hex.EncodeToString(c.SerialNumber.Bytes()) }

// Fingerprint is the SHA-256 of the DER certificate, hex encoded.
func Fingerprint(c *x509.Certificate) string {
	s := sha256.Sum256(c.Raw)
	return hex.EncodeToString(s[:])
}

// ClientTLS builds a client tls.Config trusting the system roots plus extra PEM roots, verifying serverName.
func ClientTLS(serverName string, extraRootsPEM ...[]byte) (*tls.Config, error) {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	for _, p := range extraRootsPEM {
		if len(p) > 0 && !pool.AppendCertsFromPEM(p) {
			return nil, fmt.Errorf("certs: invalid root PEM")
		}
	}
	return &tls.Config{ServerName: serverName, RootCAs: pool, MinVersion: tls.VersionTLS12}, nil
}
