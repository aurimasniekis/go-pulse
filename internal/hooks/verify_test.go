package hooks

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"
)

// GitHub's documented example ("Validating webhook deliveries"): secret "It's a Secret to Everybody",
// payload "Hello, World!", signature sha256=757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17.
func TestGitHubDocVector(t *testing.T) {
	h := http.Header{}
	h.Set("X-Hub-Signature-256", "sha256=757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17")
	if err := Verify(KindGitHub, Config{}, []byte("It's a Secret to Everybody"), h, []byte("Hello, World!"), time.Now()); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(){
		"wrong body": func() {},
		"no prefix": func() {
			h.Set("X-Hub-Signature-256", "757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17")
		},
		"sha1 header": func() { h.Del("X-Hub-Signature-256"); h.Set("X-Hub-Signature", "sha1=abc") },
		"not hex":     func() { h.Set("X-Hub-Signature-256", "sha256=zz") },
	} {
		mut()
		body := []byte("Hello, World!")
		if name == "wrong body" {
			body = []byte("Hello, World?")
		}
		if err := Verify(KindGitHub, Config{}, []byte("It's a Secret to Everybody"), h, body, time.Now()); !errors.Is(err, ErrSignature) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func stripeSig(secret, body string, ts int64) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(strconv.FormatInt(ts, 10) + "." + body))
	return hex.EncodeToString(m.Sum(nil))
}

// Stripe documents the scheme (t=…,v1=… over "<t>.<payload>", tolerance 5 minutes) but publishes no fixed secret/payload
// vector; these vectors are computed exactly as documented, including multiple v1 entries during secret rolls and the
// ignored v0 scheme.
func TestStripeScheme(t *testing.T) {
	secret, body := "whsec_test_secret", `{"id":"evt_1","type":"payment_intent.succeeded"}`
	now := time.Unix(1492774577, 0)
	good := stripeSig(secret, body, now.Unix())
	ok := []string{
		"t=1492774577,v1=" + good,
		"t=1492774577,v1=" + stripeSig("old_secret", body, now.Unix()) + ",v1=" + good + ",v0=6ffbb59b2300aae63f272406069a9788598b792a944a07aba816edb039989a39",
		" t=1492774577 , v1=" + good,
	}
	for _, hv := range ok {
		h := http.Header{"Stripe-Signature": {hv}}
		if err := Verify(KindStripe, Config{}, []byte(secret), h, []byte(body), now.Add(2*time.Minute)); err != nil {
			t.Errorf("%q: %v", hv, err)
		}
	}
	bad := map[string]string{
		"replayed late": "t=1492774577,v1=" + good,
		"v0 only":       "t=1492774577,v0=" + good,
		"no timestamp":  "v1=" + good,
		"wrong secret":  "t=1492774577,v1=" + stripeSig("nope", body, now.Unix()),
		"empty":         "",
	}
	for name, hv := range bad {
		at := now
		if name == "replayed late" {
			at = now.Add(6 * time.Minute)
		}
		if err := Verify(KindStripe, Config{}, []byte(secret), http.Header{"Stripe-Signature": {hv}}, []byte(body), at); !errors.Is(err, ErrSignature) {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestGenericHMACAndSecret(t *testing.T) {
	secret, body := []byte("s3cret"), []byte(`{"x":1}`)
	m := hmac.New(sha256.New, secret)
	m.Write(body)
	sum := m.Sum(nil)
	h := http.Header{"X-Signature": {hex.EncodeToString(sum)}}
	if err := Verify(KindHMAC, Config{}, secret, h, body, time.Now()); err != nil {
		t.Fatal(err)
	}
	h = http.Header{"X-Sig": {"sha256=" + base64.StdEncoding.EncodeToString(sum)}}
	if err := Verify(KindHMAC, Config{Header: "X-Sig", Prefix: "sha256=", Encoding: "base64"}, secret, h, body, time.Now()); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ts := strconv.FormatInt(now.Unix(), 10)
	m = hmac.New(sha256.New, secret)
	m.Write([]byte(ts + "." + string(body)))
	tc := Config{TimestampHeader: "X-Timestamp"}
	h = http.Header{"X-Signature": {hex.EncodeToString(m.Sum(nil))}, "X-Timestamp": {ts}}
	if err := Verify(KindHMAC, tc, secret, h, body, now); err != nil {
		t.Fatal(err)
	}
	if err := Verify(KindHMAC, tc, secret, h, body, now.Add(10*time.Minute)); !errors.Is(err, ErrSignature) {
		t.Fatal("stale timestamp accepted")
	}
	if err := Verify(KindHMAC, Config{}, secret, http.Header{"X-Signature": {"00"}}, body, now); !errors.Is(err, ErrSignature) {
		t.Fatal("wrong hmac accepted")
	}
	if err := Verify(KindSecret, Config{}, secret, http.Header{"X-Webhook-Secret": {"s3cret"}}, body, now); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"", "s3cre", "s3cret2"} {
		if err := Verify(KindSecret, Config{}, secret, http.Header{"X-Webhook-Secret": {v}}, body, now); !errors.Is(err, ErrSignature) {
			t.Errorf("secret %q accepted", v)
		}
	}
	if err := Verify("unknown", Config{}, secret, nil, body, now); !errors.Is(err, ErrSignature) {
		t.Fatal("unknown kind accepted")
	}
}
