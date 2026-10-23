package api

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"os"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/aurimasniekis/go-pulse/internal/acme"
	"github.com/aurimasniekis/go-pulse/internal/certs"
)

// CertSource names a certificate file pulsed knows about.
type CertSource struct {
	Name    string
	Purpose string
	Path    string // PEM file; the first certificate is described
	PEM     string // or inline PEM (the internal CA)
}

// CertificateInfo describes one certificate for the System page.
type CertificateInfo struct {
	Name      string    `json:"name"`
	Purpose   string    `json:"purpose"`
	Present   bool      `json:"present"`
	Error     string    `json:"error,omitempty"`
	Subject   string    `json:"subject,omitempty"`
	Issuer    string    `json:"issuer,omitempty"`
	DNSNames  []string  `json:"dns_names"`
	Serial    string    `json:"serial,omitempty"`
	NotBefore time.Time `json:"not_before,omitzero"`
	NotAfter  time.Time `json:"not_after,omitzero"`
	DaysLeft  int       `json:"days_left"`
}

// Certificates is the certificate overview (PL-EDGE-006).
type Certificates struct {
	Certificates []CertificateInfo `json:"certificates"`
	ACME         *ACMEStatus       `json:"acme,omitempty" doc:"Present when ACME is enabled"`
}

// ACMEStatus is the Let's Encrypt renewal state for the raw NATS hostname.
type ACMEStatus acme.Status

func describeCert(s CertSource, now time.Time) CertificateInfo {
	ci := CertificateInfo{Name: s.Name, Purpose: s.Purpose, DNSNames: []string{}}
	b := []byte(s.PEM)
	if s.Path != "" {
		var err error
		if b, err = os.ReadFile(s.Path); err != nil {
			if !os.IsNotExist(err) {
				ci.Error = err.Error()
			}
			return ci
		}
	}
	blk, _ := pem.Decode(b)
	if blk == nil {
		ci.Error = "no PEM certificate"
		return ci
	}
	c, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		ci.Error = err.Error()
		return ci
	}
	ci.Present = true
	ci.Subject, ci.Issuer, ci.Serial = c.Subject.CommonName, c.Issuer.CommonName, certs.SerialHex(c)
	ci.NotBefore, ci.NotAfter = c.NotBefore, c.NotAfter
	ci.DaysLeft = int(c.NotAfter.Sub(now).Hours() / 24)
	if c.DNSNames != nil {
		ci.DNSNames = c.DNSNames
	}
	return ci
}

func registerCertificates(api huma.API, d *Deps) {
	huma.Register(api, huma.Operation{
		OperationID: "get-certificates", Method: http.MethodGet, Path: "/system/certificates", Summary: "TLS certificates and ACME status",
		Tags: []string{"system"},
	}, func(_ context.Context, _ *struct{}) (*struct{ Body Certificates }, error) {
		out := &struct{ Body Certificates }{Body: Certificates{Certificates: []CertificateInfo{}}}
		now := time.Now()
		for _, s := range d.CertSources {
			out.Body.Certificates = append(out.Body.Certificates, describeCert(s, now))
		}
		if d.ACME != nil {
			st := ACMEStatus(d.ACME())
			out.Body.ACME = &st
		}
		return out, nil
	})
}
