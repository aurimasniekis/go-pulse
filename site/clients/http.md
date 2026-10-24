# HTTP API

For tools that cannot run a NATS client: shell scripts, cron jobs, serverless functions, home automation that only
speaks HTTP. The public API is served by `pulsed` on 443 behind Cloudflare at `https://events.example.com/api/v1`. The
full schema is [`api/public.openapi.json`](https://github.com/aurimasniekis/go-pulse/blob/main/api/public.openapi.json)
(OpenAPI 3.1).

## Tokens

Create a token on the **HTTP tokens** page. It is shown **once**, looks like `pulse_<prefix>_<secret>`, and is stored
only as a hash; the prefix is shown in the dashboard so you can recognise it.

- **Grants**: `publish`, `subscribe` and `request` on subject patterns, the same semantics as client grants.
- **Networks** (optional): IPs or CIDRs the token may be used from. With Cloudflare in front, the address checked is
  the real client address from `CF-Connecting-IP`, trusted only on connections that passed Authenticated Origin Pulls.
- **Expiry**: 365 days by default.

Send it as a bearer token:

```sh
export PULSE_TOKEN=pulse_…
curl -fsS -G https://events.example.com/api/v1/events -H "Authorization: Bearer $PULSE_TOKEN" \
  --data-urlencode 'subject=evt.home.>' -d limit=1
```

`GET /api/v1/health` is a liveness check and needs no token.

| Response | Meaning |
|---|---|
| `401` | Missing, invalid, expired or revoked token, or a request from outside the token's networks. **Counts as a failed login** towards an automatic ban of your address. |
| `403` | Valid token without the grant for this subject, or the address is banned. |
| `413` | Body above 1 MiB. |
| `422` | Invalid subject or body. |
| `429` | Rate limited: 20 requests per second per address, bursts of 40. Retry after the `Retry-After` header. |

Errors are [RFC 9457](https://www.rfc-editor.org/rfc/rfc9457) problem documents (`title`, `status`, `detail`,
`errors[]`).

## Publish one event

`POST /api/v1/publish/{subject}`. The body is the payload, stored as is. The envelope comes from headers:

| Header | Becomes |
|---|---|
| `Ce-Type` | `ce-type` (omit for an untyped event) |
| `Ce-Source` | `ce-source` (default `token:<token name>`) |
| `Idempotency-Key` | the event ID (`Nats-Msg-Id`): a retry with the same key within 2 minutes is stored once |
| `Content-Type` | `content-type` |

```sh
curl -fsS -X POST https://events.example.com/api/v1/publish/evt.home.door.opened \
  -H "Authorization: Bearer $PULSE_TOKEN" \
  -H "Ce-Type: home.door.opened.v1" -H "Content-Type: application/json" \
  -H "Idempotency-Key: $(uuidgen)" \
  -d '{"door":"front"}'
# 201 {"stream":"EVENTS","seq":1042,"id":"…","duplicate":false}
```

## Publish a batch

`POST /api/v1/publish` with up to 100 events. Every event is checked against the token's grants **before any is
published**; one denied subject rejects the whole batch.

```sh
curl -fsS -X POST https://events.example.com/api/v1/publish \
  -H "Authorization: Bearer $PULSE_TOKEN" -H "Content-Type: application/json" \
  -d '{"events":[
        {"subject":"evt.home.temp.measured.kitchen","type":"home.temp.measured.v1","data":{"c":21.5}},
        {"subject":"evt.home.temp.measured.porch","type":"home.temp.measured.v1","data":{"c":7.0},"id":"porch-2026-10-24T08:00"},
        {"subject":"evt.home.camera.note.added","data":"plain text is stored as text/plain"},
        {"subject":"evt.home.camera.raw.captured","data_base64":"AAEC"}
      ]}'
# 200 {"results":[{"stream":"EVENTS","seq":1043,…}, …]}
```

Per event: `subject`, `type`, `source`, `id` (idempotency key), `content_type`, and either `data` (any JSON value; a JSON
string is stored as text) or `data_base64` (binary).

## Request/reply bridge

`POST /api/v1/request/{subject}?timeout_ms=5000` sends the body as a NATS request to a service listening on the subject
and returns the reply body with its content type. Needs a `request` grant. `timeout_ms` is 100–30000 (default 5000).

```sh
curl -fsS -X POST "https://events.example.com/api/v1/request/svc.weather.now?timeout_ms=2000" \
  -H "Authorization: Bearer $PULSE_TOKEN" -H "Content-Type: application/json" -d '{"city":"Vilnius"}'
```

`503` means nobody serves the subject; `504` means no reply within the timeout.

## Replay

`GET /api/v1/events?subject=<filter>&since=<RFC 3339>&seq=<n>&limit=<1-500>&cursor=<next>` returns stored events oldest
first. Needs a `subscribe` grant covering `subject`. Page with the `next` cursor until it is absent.

```sh
curl -fsS -G https://events.example.com/api/v1/events \
  -H "Authorization: Bearer $PULSE_TOKEN" \
  --data-urlencode 'subject=evt.home.>' --data-urlencode 'since=2026-10-01T00:00:00Z' -d limit=100
```

```json
{
  "events": [
    {
      "stream": "EVENTS", "seq": 1042, "subject": "evt.home.door.opened",
      "stream_time": "2026-10-24T08:00:01.123Z", "size": 16,
      "headers": {"ce-type": ["home.door.opened.v1"], "…": ["…"]},
      "data": "{\"door\":\"front\"}", "encoding": "utf8",
      "id": "01J…", "type": "home.door.opened.v1", "source": "door-bot",
      "content_type": "application/json", "event_time": "2026-10-24T08:00:01.1Z",
      "untyped": false, "subject_valid": true
    }
  ],
  "next": "…", "scanned": 1, "scanned_bytes": 16, "partial": false, "first_seq": 1, "last_seq": 2210
}
```

`data` is the payload as UTF-8 text, or base64 when `encoding` is `base64`. When `partial` is true the scan budget ran
out before the page filled; continue with `next`.

## Live tail (SSE)

`GET /api/v1/tail?subject=<filter>&since=<RFC 3339>` streams new events as
[server-sent events](https://html.spec.whatwg.org/multipage/server-sent-events.html). Needs a `subscribe` grant (the
filter defaults to `evt.>`).

```sh
curl -N -fsS -G https://events.example.com/api/v1/tail \
  -H "Authorization: Bearer $PULSE_TOKEN" --data-urlencode 'subject=evt.home.>'
```

- `event: event` messages carry the same JSON as replay, with `id:` set to the stream sequence. Reconnect with
  `Last-Event-ID: <seq>` to resume right after it.
- `event: notice` messages report dropped events (`dropped`, `reason`: `rate` or `slow_client`). A session delivers at
  most 200 events per second; at most 10 tail sessions run at once across the server.
- A keepalive comment arrives every 20 seconds, so Cloudflare does not close the idle connection after 100 seconds.

For complete, gap-free history use replay; the tail is for watching.

## Limits

| Limit | Value |
|---|---|
| Body size | 1 MiB on `/api/v1` (store larger payloads as blobs through a NATS client) |
| Rate | 20 requests/s per address, burst 40 |
| Batch | 100 events |
| Replay page | 500 events |
| Request bridge timeout | 30 s |
