// Package hooks receives inbound webhooks: verifies signatures, then publishes the request as an event
// (PL-HOOK-001..004).
package hooks

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Kinds of verifier.
const (
	KindGitHub = "github"
	KindStripe = "stripe"
	KindHMAC   = "hmac"
	KindSecret = "secret"
)

// ErrSignature is the only verification error callers see (no detail leaks to senders).
var ErrSignature = errors.New("invalid signature")

// Config tunes the hmac and secret verifiers (ignored by github and stripe, which follow the providers' formats).
type Config struct {
	Header          string `json:"header,omitempty" doc:"Signature/secret header (hmac default X-Signature, secret default X-Webhook-Secret)"`
	Encoding        string `json:"encoding,omitempty" enum:"hex,base64," doc:"hmac digest encoding (default hex)"`
	Prefix          string `json:"prefix,omitempty" doc:"Prefix before the digest, e.g. sha256="`
	TimestampHeader string `json:"timestamp_header,omitempty" doc:"hmac: header with a unix timestamp; signs '<ts>.<body>' and enforces the tolerance"`
	ToleranceSec    int    `json:"tolerance_sec,omitempty" doc:"Accepted clock skew for timestamped signatures (default 300)"`
	Type            string `json:"type,omitempty" doc:"ce-type for published events (github/stripe derive it from the payload)"`
}

func (c Config) tolerance() time.Duration {
	if c.ToleranceSec > 0 {
		return time.Duration(c.ToleranceSec) * time.Second
	}
	return 5 * time.Minute
}

func hmacSHA256(secret, msg []byte) []byte {
	m := hmac.New(sha256.New, secret)
	m.Write(msg)
	return m.Sum(nil)
}

// Verify checks a request (headers + raw body) against the hook's secret.
func Verify(kind string, c Config, secret []byte, h http.Header, body []byte, now time.Time) error {
	switch kind {
	case KindGitHub:
		// X-Hub-Signature-256: sha256=<hex HMAC-SHA256(secret, body)>
		sig, ok := strings.CutPrefix(h.Get("X-Hub-Signature-256"), "sha256=")
		got, err := hex.DecodeString(sig)
		if !ok || err != nil || !hmac.Equal(got, hmacSHA256(secret, body)) {
			return ErrSignature
		}
		return nil
	case KindStripe:
		return verifyStripe(h.Get("Stripe-Signature"), secret, body, c.tolerance(), now)
	case KindHMAC:
		header := c.Header
		if header == "" {
			header = "X-Signature"
		}
		sig, ok := strings.CutPrefix(h.Get(header), c.Prefix)
		if !ok || sig == "" {
			return ErrSignature
		}
		var got []byte
		var err error
		if c.Encoding == "base64" {
			got, err = base64.StdEncoding.DecodeString(sig)
		} else {
			got, err = hex.DecodeString(sig)
		}
		if err != nil {
			return ErrSignature
		}
		msg := body
		if c.TimestampHeader != "" {
			ts, err := strconv.ParseInt(h.Get(c.TimestampHeader), 10, 64)
			if err != nil || absDur(now.Sub(time.Unix(ts, 0))) > c.tolerance() {
				return ErrSignature
			}
			msg = append([]byte(strconv.FormatInt(ts, 10)+"."), body...)
		}
		if !hmac.Equal(got, hmacSHA256(secret, msg)) {
			return ErrSignature
		}
		return nil
	case KindSecret:
		header := c.Header
		if header == "" {
			header = "X-Webhook-Secret"
		}
		v := []byte(h.Get(header))
		if len(v) == 0 || subtle.ConstantTimeCompare(v, secret) != 1 {
			return ErrSignature
		}
		return nil
	}
	return ErrSignature
}

// verifyStripe follows Stripe's documented scheme: Stripe-Signature: t=<unix>,v1=<hex>[,v1=…][,v0=…]; the signed
// payload is "<t>.<body>" with HMAC-SHA256 and the endpoint secret; any v1 may match; t must be within the tolerance.
func verifyStripe(header string, secret, body []byte, tol time.Duration, now time.Time) error {
	var ts int64 = -1
	var sigs [][]byte
	for _, part := range strings.Split(header, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch k {
		case "t":
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return ErrSignature
			}
			ts = n
		case "v1":
			if b, err := hex.DecodeString(v); err == nil {
				sigs = append(sigs, b)
			}
		}
	}
	if ts < 0 || len(sigs) == 0 || absDur(now.Sub(time.Unix(ts, 0))) > tol {
		return ErrSignature
	}
	want := hmacSHA256(secret, append([]byte(strconv.FormatInt(ts, 10)+"."), body...))
	for _, s := range sigs {
		if hmac.Equal(s, want) {
			return nil
		}
	}
	return ErrSignature
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
