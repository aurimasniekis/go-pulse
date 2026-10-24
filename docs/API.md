# HTTP APIs

> PL-API-001..005, PL-DOC-002. Specs are generated from code: `make api` writes `api/admin.openapi.json` and
> `api/public.openapi.json` (OpenAPI 3.1) and the UI's TypeScript types; CI fails on drift.

## Listeners

| Listener | Default bind | Base path | Who |
|---|---|---|---|
| admin | `127.0.0.1:8080` (published on host loopback only) | `/api/admin/v1` + the dashboard at `/` | Operator through `ssh -L 8080:127.0.0.1:8080 <vps>` |
| public | `:8443` (443 via Cloudflare, AOP mTLS in prod) | `/api/v1`, `/hooks/*`, `/ws`, `/healthz` | Tools with HTTP tokens, webhook senders, browsers |
| metrics | `127.0.0.1:9464` | `/metrics`, `/healthz`, `/readyz`, `/internal/v1/bans` | Alloy, container healthcheck, banagent |

Every other path on the public listener returns 404 (PL-EDGE-001). The public listener applies a per-IP token bucket
(20 req/s, burst 40) and body limits (1 MiB API, 5 MiB hooks).

## Conventions

- JSON bodies; errors are RFC 9457 problem details (`title`, `status`, `detail`, `errors[]`). 5xx responses never
  include internal error text (it is logged).
- Admin writes (`POST`, `PUT`, `PATCH`, `DELETE`) require `X-Pulse-CSRF: 1` (PL-ADM-004).
- The admin listener only accepts `Host` values `127.0.0.1`, `localhost`, `::1` and `PULSE_ADMIN_HOSTS`; anything else
  gets 421 (DNS-rebinding guard, THREAT_MODEL T-21).
- Destructive admin calls take `?confirm=<name>` repeating the target's name.
- Live data uses SSE: `event` messages carry `id: <stream seq>`; reconnect with `Last-Event-ID` to resume. A comment
  keepalive is sent every 20 s (Cloudflare closes idle connections after 100 s).

## Admin API

| Method | Path | Purpose |
|---|---|---|
| GET | `/system/info` | Version, profile, auth mode, NATS connection and server stats (VARZ), JetStream usage, pulsed runtime |
| GET | `/system/connections` | NATS connections across accounts (SYS, APP), busiest first (`sort`, `limit`) |
| GET | `/system/certificates` | Internal CA, NATS leaf, Let's Encrypt, origin and AOP CA certificates (names, issuer, serial, expiry, days left) plus ACME status when enabled (ADR-0032) |
| GET | `/streams` | Streams in the APP account (`kind`: stream, kv, object; `managed` for EVENTS and blobs) |
| GET/PUT/DELETE | `/streams/{name}` | Read, update, delete a user stream (EVENTS and blobs are managed) |
| POST | `/streams` | Create a user stream; subjects may not overlap `evt.>`, `_sys.>`, `$…`, `_INBOX.>` |
| GET | `/streams/{name}/consumers` | Consumers with pending/ack-pending/redelivery counts |
| DELETE | `/streams/{name}/consumers/{consumer}` | Delete a consumer (`?confirm=`) |
| GET | `/events` | Explorer page (see below) |
| GET | `/events/{stream}/{seq}` | One event |
| GET | `/tail` | Live tail (SSE) |
| POST | `/events` | Publish one event as the operator (subject grammar enforced, audited) |
| GET | `/overview` | KPIs, top domains/types/subjects, disk forecast, subject index stats |
| GET | `/throughput` | Time series: `range` 1h/24h/7d/30d/1y, `dim` total/type/p1–p4, `top` N keys |
| GET | `/subjects/tree` | One level of the subject tree below `prefix` |
| GET | `/subjects/info` | One indexed subject with sampled JSON keys |
| GET/POST | `/clients` | List clients (with live connection counts) / create a client and return its `.creds` once |
| GET/PUT | `/clients/{id}` | Read / update name, description, tags (optimistic `version`) |
| PUT | `/clients/{id}/grants` | Replace grants (no new creds; NATS reconnects live sessions) |
| POST | `/clients/{id}/rotate` · `/revoke` · `/kick` | New creds (old revoked) · permanent revoke · disconnect |
| GET | `/clients/{id}/connections` | Live connections from CONNZ |
| GET/POST | `/tokens` | List / create an HTTP token (secret returned once) |
| POST | `/tokens/{id}/revoke` | Revoke a token |
| GET | `/audit` | Audit log, newest first; filters `action` (exact or `prefix.`), `actor`, `target`, `result`, `since`, `until`, `before_id` |

### Explorer (`GET /events`)

Query: `stream` (default EVENTS), `subject` (repeatable, wildcards), `dir` (`backward` = newest first, default;
`forward`), `start_seq`, `start_time` (forward only), `limit` (1–500), `type` (exact, or `(untyped)`), `header`
(`key=value`, repeatable), `contains` (payload substring), `json_path` + `json_value`, `cursor`.

Response: `events[]`, `next` (opaque cursor bound to the filters; reusing it with other filters is a 400), `scanned`,
`scanned_bytes`, `partial` + `reason` (`msgs`, `bytes`, `time`) when the scan budget (50k messages, 64 MB, 3 s) ran out
before the page filled — continue with `next`. At most 4 queries run at once (429 otherwise).

Each event: `stream`, `seq`, `subject`, `stream_time`, `size`, `headers`, `data` + `encoding` (`utf8` or `base64`),
envelope fields (`id`, `type`, `source`, `content_type`, `event_time`), `untyped`, `subject_valid`, `blob`.

### Live tail (`GET /tail`)

Query: `stream`, `subject` (repeatable), `since`. Header: `Last-Event-ID`. At most 10 sessions; 200 events/s per
session — excess events are dropped and reported in a `notice` (`dropped`, `reason`: `rate` or `slow_client`).

### Rollups

Counts come from headers-only consumers and are committed every 10 s together with the consumer checkpoint
(exactly once). Minute data covers 8 days, hourly 400 days, daily forever. The retention horizon is
`max_bytes / average daily bytes` and is meaningful after a day of traffic.

## Public API

Every route needs `Authorization: Bearer pulse_<prefix>_<secret>`. A missing or invalid token gets 401 (and counts as a
failed login for the ban detector); a valid token without the needed grant gets 403. Tokens may be limited to
source networks.

| Method | Path | Grant | Purpose |
|---|---|---|---|
| POST | `/api/v1/publish/{subject}` | publish | Body = payload. Headers: `Ce-Type`, `Ce-Source` (default `token:<name>`), `Idempotency-Key` (→ `Nats-Msg-Id`), `Content-Type`. 201 with `{stream, seq, id, duplicate}`. |
| POST | `/api/v1/publish` | publish (each) | `{events: [...]}`, up to 100; all are authorized before any is published. `data` (JSON value; a JSON string is stored as text) or `data_base64`. |
| POST | `/api/v1/request/{subject}?timeout_ms=` | request | NATS request/reply; returns the reply body and content type. 503 no responders, 504 timeout (max 30 s). |
| GET | `/api/v1/events?subject=&since=&seq=&limit=&cursor=` | subscribe | Replay oldest first, paged. `subject` must be within a grant. |
| GET | `/api/v1/tail?subject=&since=` | subscribe | SSE live events; resume with `Last-Event-ID`. |
| GET | `/api/v1/health` | none | Liveness. |

Payloads above ~1 MiB get 413: store them as blobs (NATS clients) and publish a reference.

## Webhooks

### Inbound: `POST /hooks/{name}` (public listener)

Verified requests are published to the hook's subject (default `evt.hook.<name>.received`) and answered with
`202 {"seq": N, "duplicate": bool}`. Bad or missing signatures get 401 (no detail) and count toward bans; unknown or
disabled hooks get 404; bodies above 5 MiB get 413.

| Verifier | Check | Event fields |
|---|---|---|
| `github` | `X-Hub-Signature-256: sha256=<hex HMAC-SHA256(secret, body)>` | type `github.<X-GitHub-Event>`, id = `X-GitHub-Delivery` (2-minute de-duplication of redeliveries) |
| `stripe` | `Stripe-Signature: t=…,v1=…` over `"<t>.<body>"`, any `v1` may match, 5-minute tolerance | type `stripe.<type>`, id = event `id` |
| `hmac` | configurable header, hex/base64, optional prefix, optional timestamp header (signs `"<ts>.<body>"`) | type from config or `hook.<name>`, id = `Idempotency-Key` |
| `secret` | constant-time compare of a header value | as `hmac`; no replay protection — use only when the sender cannot sign |

### Outbound

Each outbound hook is a durable consumer on `EVENTS` that starts at events published after creation and delivers one
event at a time, in stream order. Requests: `POST <url>`, body = event payload, `Content-Type` from the event, plus
`Pulse-Signature: t=<unix>,v1=<hex HMAC-SHA256(secret, "<t>.<body>")>`, `Pulse-Event-Id`, `Pulse-Event-Type`,
`Pulse-Event-Source`, `Pulse-Event-Time`, `Pulse-Subject`, `Pulse-Stream-Seq`, `Pulse-Delivery-Attempt` and configured
headers. A 2xx response acknowledges; anything else is retried after 1 s, 2 s, 5 s, 15 s, 30 s, 1 m, 5 m, 10 m, 20 m,
40 m and 1 h (12 attempts), then recorded as dead and skipped. Delivery is at least once: receivers should de-duplicate
on `Pulse-Event-Id`. Destinations that resolve to loopback, private, link-local, CGNAT, multicast or metadata addresses
are refused unless allowlisted with `PULSE_OUTBOUND_ALLOW`.

Admin endpoints: `/hooks/inbound` (CRUD, `rotate_secret`), `/hooks/outbound` (CRUD, `/pause`, `/resume`, `/test`,
`/deliveries`), `/hooks/deliveries/{id}/retry`. Secrets are returned only when created or rotated.

## Edge

- **Real client IP:** `CF-Connecting-IP` is used only when the TCP peer is in Cloudflare's ranges *and* the connection
  presented a client certificate that verified against `PULSE_AOP_CA_FILE`; otherwise the peer address is used.
- **Bans:** banned addresses get 403 on every public route. Missing/invalid tokens and bad webhook signatures count as
  failures; so do NATS `CLIENT.AUTH.ERR` advisories.
- **`/ws`:** proxies to the NATS websocket listener for browser clients (`ws(s)://<host>/ws` with creds). The proxy
  replaces `X-Forwarded-For` with exactly the real client IP, because NATS trusts the first value.

Admin: `GET /security` (edge status, active bans, history, recent failures, offenders, allowlist), `POST /security/bans`,
`POST /security/bans/lift`, `POST /security/allowlist`, `POST /security/allowlist/remove`.

## Alerts (PL-OBS-004..006, ADR-0033)

Admin: `GET /alerts` (rules with live instances, notifier counters, firing count), `POST /alerts/rules` (custom threshold
rule), `PUT /alerts/rules/{id}`, `DELETE /alerts/rules/{id}` (custom only; built-ins return 409), `POST /alerts/evaluate`,
`GET /alerts/history?rule=&before_id=&limit=`, `POST /alerts/notifiers/{name}/test` (502 with the reason on failure).

Built-in rules (ids = kinds): `disk_forecast` (30 d), `stream_usage` (85 %), `cert_expiry` (14 d), `backup_stale` (36 h,
reports *no data* until backups exist), `client_expiry` (30 d, PL-AUTH-012), `ban_storm` (10 in 10 min),
`slow_consumers` (new ones in 10 min), `dead_letters` (24 h), `nats_down` (held 60 s). Custom `threshold` rules count
events or payload bytes for the whole stream, one ce-type or one subject prefix over a window, `above` or `below` a
threshold (`below 1` is a dead-man switch). Prefixes of 1–2 tokens are counted per minute, 3 tokens per hour, 4 per day;
the window must be a multiple of that step, and only completed buckets count.

Each firing/resolved transition is written to the history and published (core NATS, APP account, not persisted) as:

```
subject:  _sys.alert.<rule id>                       e.g. _sys.alert.cert_expiry, _sys.alert.custom_xxxxx
ce-type:  pulse.alert.firing.v1 | pulse.alert.resolved.v1
ce-source: pulsed
data:     {"rule_id","rule_name","kind","key","state","severity","summary","value","since","at"}
```

Tools subscribe with a `subscribe` grant on `_sys.alert.>` (the only grantable `_sys` subject).

## Metrics (`GET /metrics` on the metrics listener, PL-OBS-002)

| Metric | Type | Labels |
|---|---|---|
| `pulse_build_info` | gauge | `version` |
| `pulse_http_requests_total` | counter | `listener`, `route` (first path segment only), `method`, `code` |
| `pulse_http_request_duration_seconds` | histogram | `listener`, `route` |
| `pulse_ingest_events_total` | counter | `via` = http, hook, admin |
| `pulse_rollup_events_total`, `pulse_rollup_bytes_total` | counter | `stream` (all publishers, incl. raw NATS) |
| `pulse_auth_failures_total`, `pulse_bans_total` | counter | `source` |
| `pulse_bans_active` | gauge | — |
| `pulse_inbound_hooks_total`, `pulse_outbound_deliveries_total` | counter | `result` / `outcome` |
| `pulse_alerts_firing`, `pulse_alert_transitions_total`, `pulse_notifications_total` | gauge, counter, counter | `severity` / `rule`,`state` / `notifier`,`result` |
| `pulse_certificate_not_after_timestamp_seconds` | gauge | `certificate` |
| `pulse_acme_attempts_total`, `pulse_tail_sessions`, `pulse_nats_connected` | counter, gauge, gauge | `result` |
| `pulse_stream_{bytes,max_bytes,messages,consumers,first_timestamp_seconds}` | gauge | `stream` |
| `nats_up`, `nats_connections`, `nats_connections_total`, `nats_subscriptions`, `nats_*_msgs_total`, `nats_*_bytes_total`, `nats_slow_consumers_total`, `nats_mem_bytes`, `nats_cpu_percent` | gauge/counter | — (VARZ at scrape time, cached 5 s) |
| `nats_jetstream_{storage,storage_max,memory}_bytes`, `nats_jetstream_{streams,consumers,messages}`, `nats_jetstream_api_{requests,errors}_total` | gauge/counter | — (JSZ) |

Plus the Go and process collectors (`go_*`, `process_*`).

## Backups (PL-BAK-006/008, ADR-0035)

Admin: `GET /backups` (enabled, destination, schedule, retention, recipients, running operation, catalog, last
restore), `POST /backups/run` (202; 409 while busy), `POST /backups/prune`, `GET /backups/remote` (complete backups in the
object store, including earlier servers'), `POST /backups/{id}/plan` (dry run) and `POST /backups/{id}/restore`
(202, background job; `replace` needs `?confirm=<id>`). Plan and restore take `{"identity": "AGE-SECRET-KEY-1…",
"streams": [...], "replace": bool}`; the identity is used for that request only. A wrong identity, or an object that
fails its integrity check, returns 422.

## KV and objects (PL-UI-004)

Admin: `GET /kv` (KV and object buckets), `POST /kv`, `DELETE /kv/{bucket}?confirm=`, `GET /kv/{bucket}/keys?filter=&limit=`
(latest revision per key; `truncated` when more exist), `GET|PUT|DELETE /kv/{bucket}/entry?key=` (value as utf8 or base64,
history; `?purge=true` drops history), `POST /objects`, `DELETE /objects/{bucket}?confirm=`, `GET /objects/{bucket}`,
`GET /objects/{bucket}/download?name=` (attachment, `nosniff`), `PUT /objects/{bucket}/upload?name=` (raw body ≤ 64 MiB),
`DELETE /objects/{bucket}/object?name=`. The managed `blobs` store is read-only here (409). Every change is audited.

`GET /system/config` returns the effective configuration for the Settings page; secrets appear only as set / not set.

