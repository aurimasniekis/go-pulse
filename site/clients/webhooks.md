# Webhooks in and out

**Inbound** webhooks let other services send events without a Pulse credential: `pulsed` verifies the sender's
signature and publishes the request body as an event. **Outbound** webhooks deliver events to your own HTTP endpoints,
signed, in order and with retries. Both are managed on the **Webhooks** page.

<Shot name="webhooks" alt="Inbound and outbound webhooks with the delivery log" caption="Webhooks" />

## Inbound

Each inbound hook has a name, a verifier, a secret and a subject. The sender posts to:

```
https://events.example.com/hooks/<name>
```

| Response | Meaning |
|---|---|
| `202 {"seq": N, "duplicate": bool}` | Verified and stored. |
| `401` | Missing or bad signature. No detail is given, and the failure **counts towards a ban** of the sender's address. |
| `404` | Unknown or disabled hook. |
| `413` | Body too large: above 1 MiB (the NATS `max_payload`) a verified body cannot be stored; above 5 MiB the request is cut off. |
| `503` | Not stored: NATS is temporarily unavailable. |

The event goes to the hook's subject, by default `evt.hook.<name>.received`. Its source is `hook:<name>`, the body is
the payload unchanged, and the content type is the request's.

### Verifiers

| Verifier | What it checks | Event type and ID |
|---|---|---|
| `github` | `X-Hub-Signature-256: sha256=<hex HMAC-SHA256(secret, body)>` | type `github.<X-GitHub-Event>`; ID `X-GitHub-Delivery`, so redeliveries within 2 minutes are stored once. The GitHub event, delivery and hook ID headers are kept. |
| `stripe` | `Stripe-Signature: t=…,v1=…` over `<t>.<body>`; any `v1` may match; `t` within 5 minutes | type `stripe.<type>`; ID the Stripe event `id` |
| `hmac` | HMAC-SHA256 in a header you choose (default `X-Signature`), hex or base64, with an optional prefix such as `sha256=`; optionally a timestamp header, in which case the signed message is `<ts>.<body>` and the timestamp must be within the tolerance (default 300 s) | type from the hook's config, else `hook.<name>`; ID from `Idempotency-Key` |
| `secret` | A shared secret compared in constant time against a header (default `X-Webhook-Secret`) | as `hmac` |

Use `secret` only when the sender cannot sign: it has no replay protection, and anyone who sees one request can send
more. Prefer `hmac` with a timestamp header for your own senders.

When you create a hook you can paste the provider's secret (at least 16 characters) or let `pulsed` generate one. The
secret is shown **once**, stored sealed with the master key, and can be rotated.

### Setting up senders

**GitHub** (repository or organization → Settings → Webhooks → Add webhook):

1. Create an inbound hook named `github` with verifier `github` and copy the generated secret.
2. Payload URL `https://events.example.com/hooks/github`, content type `application/json`, the secret, and the events
   you want.
3. GitHub's ping arrives as type `github.ping`. A push arrives as `github.push` on `evt.hook.github.received`, or on the
   subject you set, e.g. `evt.github.repo.push.go-pulse` (one hook per repository).

**Stripe** (Developers → Webhooks → Add endpoint):

1. Endpoint URL `https://events.example.com/hooks/stripe`; Stripe shows a signing secret `whsec_…`.
2. Create an inbound hook named `stripe` with verifier `stripe` and paste that secret.

**Your own sender** with `hmac` and a timestamp header `X-Timestamp`:

```sh
body='{"backup":"ok"}'; ts=$(date +%s)
sig=$(printf '%s.%s' "$ts" "$body" | openssl dgst -sha256 -hmac "$HOOK_SECRET" -hex | sed 's/^.* //')
curl -fsS -X POST https://events.example.com/hooks/nas-backup \
  -H "Content-Type: application/json" -H "X-Timestamp: $ts" -H "X-Signature: $sig" \
  -H "Idempotency-Key: nas-$(date +%F)" -d "$body"
```

Cloudflare sits in front of `/hooks/*`. If a provider's requests are blocked, check the WAF events for that hostname.

## Outbound

Each outbound hook has a name, a **filter** under `evt.>` (e.g. `evt.home.door.>`), a destination URL, optional extra
headers and a signing secret (`whsec_…`, generated unless you set one; shown once). It is a durable consumer on
`EVENTS` that starts with events published **after** the hook is created and delivers one event at a time, in stream
order.

### The request

`POST <url>` with the event payload as the body and the event's content type, plus:

| Header | Value |
|---|---|
| `Pulse-Signature` | `t=<unix seconds>,v1=<hex HMAC-SHA256(secret, "<t>.<body>")>` |
| `Pulse-Event-Id` | The event ID; use it to de-duplicate |
| `Pulse-Event-Type`, `Pulse-Event-Source`, `Pulse-Event-Time` | The envelope (when set) |
| `Pulse-Subject`, `Pulse-Stream-Seq` | Where it was stored |
| `Pulse-Delivery-Attempt` | 1 for the first attempt |
| your extra headers | as configured |

Any `2xx` within 10 seconds acknowledges the event. Anything else is retried after 1 s, 2 s, 5 s, 15 s, 30 s, 1 min,
5 min, 10 min, 20 min, 40 min and 1 h: 12 attempts over a little more than two hours. After the last one, the event is
recorded as **dead**, the `dead_letters` alert fires, and the hook moves on. Because delivery is in order, a failing
endpoint holds back later events until it recovers or the event dies.

Delivery is **at least once**: de-duplicate on `Pulse-Event-Id`.

### Verifying the signature

Check the HMAC over the raw body, compare in constant time, and reject old timestamps:

```go
func verify(secret []byte, r *http.Request, body []byte) bool {
	var ts, sig string
	for _, part := range strings.Split(r.Header.Get("Pulse-Signature"), ",") {
		if v, ok := strings.CutPrefix(part, "t="); ok {
			ts = v
		} else if v, ok := strings.CutPrefix(part, "v1="); ok {
			sig = v
		}
	}
	t, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || time.Since(time.Unix(t, 0)).Abs() > 5*time.Minute {
		return false
	}
	m := hmac.New(sha256.New, secret) // the whole secret string, including "whsec_"
	m.Write([]byte(ts + "."))
	m.Write(body)
	want := hex.EncodeToString(m.Sum(nil))
	return hmac.Equal([]byte(want), []byte(sig))
}
```

```python
import hashlib, hmac, time

def verify(secret: bytes, header: str, body: bytes) -> bool:
    parts = dict(p.split("=", 1) for p in header.split(",") if "=" in p)
    ts, sig = parts.get("t", ""), parts.get("v1", "")
    if not ts.isdigit() or abs(time.time() - int(ts)) > 300:
        return False
    want = hmac.new(secret, f"{ts}.".encode() + body, hashlib.sha256).hexdigest()
    return hmac.compare_digest(want, sig)
```

Retries are signed again with a fresh timestamp.

### Managing deliveries

On the Webhooks page, per outbound hook: pending events, the last delivery, the **delivery log** (attempt, outcome,
status code, latency, a short response excerpt), **pause** and **resume**, **send a test event**, and **retry** a dead
delivery. Error messages in the log never include the destination URL, since URLs often carry secrets.

### Destinations Pulse refuses

To prevent server-side request forgery, `pulsed` checks the resolved address of every connection, including after
redirects. Loopback, private (RFC 1918, ULA), link-local, CGNAT, multicast and cloud metadata addresses are refused.
To deliver to a service on your own network, allowlist it with `PULSE_OUTBOUND_ALLOW` (IPs or CIDRs); see the
[configuration reference](/reference/configuration).
