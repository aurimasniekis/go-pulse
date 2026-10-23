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

## Admin API (M3 scope)

| Method | Path | Purpose |
|---|---|---|
| GET | `/system/info` | Version, profile, auth mode, NATS connection, JetStream usage |
| GET | `/streams` | Streams in the APP account (`kind`: stream, kv, object; `managed` for EVENTS and blobs) |
| GET/PUT/DELETE | `/streams/{name}` | Read, update, delete a user stream (EVENTS and blobs are managed) |
| POST | `/streams` | Create a user stream; subjects may not overlap `evt.>`, `_sys.>`, `$…`, `_INBOX.>` |
| GET | `/streams/{name}/consumers` | Consumers with pending/ack-pending/redelivery counts |
| DELETE | `/streams/{name}/consumers/{consumer}` | Delete a consumer (`?confirm=`) |
| GET | `/events` | Explorer page (see below) |
| GET | `/events/{stream}/{seq}` | One event |
| GET | `/tail` | Live tail (SSE) |

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

## Public API

M3 ships `GET /api/v1/health`. Token publish, the request bridge, replay and SSE tail arrive with M6; hooks with M7;
`/ws` with M8.
