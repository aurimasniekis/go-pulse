package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/aurimasniekis/go-pulse/internal/acme"
)

func TestCertificates(t *testing.T) {
	h := newHarness(t)
	h.deps.CertSources = []CertSource{
		{Name: "Internal CA", PEM: h.env.Keys.CACert},
		{Name: "NATS internal leaf", Path: h.env.Layout.ServerCert()},
		{Name: "NATS Let's Encrypt", Path: h.env.Layout.ACMECert()},
	}
	h.deps.ACME = func() acme.Status { return acme.Status{Domain: "r.pulse.test", LastError: "dns timeout"} }
	resp, body := do(t, http.MethodGet, h.admin.URL+AdminPrefix+"/system/certificates", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	var out Certificates
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Certificates) != 3 || out.ACME == nil || out.ACME.LastError != "dns timeout" {
		t.Fatalf("%s", body)
	}
	ca, leaf, le := out.Certificates[0], out.Certificates[1], out.Certificates[2]
	if !ca.Present || !leaf.Present || leaf.Issuer != ca.Subject || leaf.DaysLeft < 1 || leaf.NotAfter.Before(time.Now()) {
		t.Fatalf("ca %+v leaf %+v", ca, leaf)
	}
	if le.Present || le.Error != "" {
		t.Fatalf("missing ACME cert must be not present without an error: %+v", le)
	}
}
