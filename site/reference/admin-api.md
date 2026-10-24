# Admin API

The dashboard is a client of the admin API, and everything it does is available to scripts through the SSH tunnel.
The full OpenAPI 3.1 description is generated from the code:
[`api/admin.openapi.json`](https://github.com/aurimasniekis/go-pulse/blob/main/api/admin.openapi.json). The public
API that tools use is described in [HTTP API](/clients/http)
([`api/public.openapi.json`](https://github.com/aurimasniekis/go-pulse/blob/main/api/public.openapi.json)).

## Listener and access

| | |
|---|---|
| Listener | `PULSE_ADMIN_ADDR`, default `127.0.0.1:8080`; published on host loopback only |
| Base path | `/api/admin/v1` (the dashboard is served at `/`) |
| Authentication | None (`PULSE_AUTH_MODE=none`). Reach it with `ssh -L 8080:127.0.0.1:8080 <vps>` |

Because there is no login, the listener defends against the browser being used against it:

- **CSRF header.** Every `POST`, `PUT`, `PATCH` and `DELETE` must send `X-Pulse-CSRF: 1` (without it: 403). Browsers cannot add it to a
  cross-site form or a simple request.
- **Host allowlist.** Only `Host` values `127.0.0.1`, `localhost`, `::1` (with any port) and `PULSE_ADMIN_HOSTS` are
  accepted; anything else gets **421**. This stops DNS-rebinding pages from talking to the tunnel.
- **No CORS**, a strict Content-Security-Policy and `X-Frame-Options: DENY` on every response.
- **Confirmation.** Destructive calls take `?confirm=<name>` that repeats the target's name: deleting a stream, a
  consumer, a KV bucket or an object store, and a restore with `replace` (the backup id).

Every change is written to the audit log with the actor.

```sh
curl -s http://127.0.0.1:8080/api/admin/v1/system/info | jq .version
curl -s -X POST http://127.0.0.1:8080/api/admin/v1/backups/run -H 'X-Pulse-CSRF: 1'
```

## Conventions

- JSON request and response bodies.
- Errors are RFC 9457 problem details (`title`, `status`, `detail`, `errors[]`). 5xx responses never contain internal
  error text; it is logged instead. A request the browser canceled is logged as 499.
- Updates of clients carry the object's `version`: a stale version gets 409 (optimistic concurrency).
- Secrets (client creds, token values, hook secrets) appear only in the response that creates or rotates them.
- **Live data uses Server-Sent Events.** `event` messages carry `id: <stream sequence>`; reconnect with
  `Last-Event-ID` to resume. A comment keepalive is sent every 20 seconds.

## Endpoints

### System

| Method | Path | Purpose |
|---|---|---|
| GET | `/system/info` | Version, profile, auth mode, NATS connection and server stats, JetStream usage, pulsed runtime, connect info |
| GET | `/system/connections` | NATS connections across accounts, busiest first (`sort`, `limit`) |
| GET | `/system/certificates` | Every certificate (names, issuer, serial, expiry, days left) and the ACME status |
| GET | `/system/config` | Effective configuration for the Settings page; secrets only as set / not set |

### Events and streams

| Method | Path | Purpose |
|---|---|---|
| GET | `/events` | Explorer page (filters below) |
| GET | `/events/{stream}/{seq}` | One event |
| POST | `/events` | Publish one event as the operator (subject grammar enforced, audited) |
| GET | `/tail` | Live tail (SSE): `stream`, `subject` (repeatable), `since`, `Last-Event-ID` |
| GET | `/streams` | Streams in the APP account (`kind`: stream, kv, object; `managed` for `EVENTS` and `blobs`) |
| POST | `/streams` | Create a user stream (subjects may not overlap `evt.>`, `_sys.>`, `$…`, `_INBOX.>`) |
| GET, PUT, DELETE | `/streams/{name}` | Read, update, delete a user stream (`DELETE` needs `confirm`) |
| GET | `/streams/{name}/consumers` | Consumers with pending, ack-pending and redelivery counts |
| DELETE | `/streams/{name}/consumers/{consumer}` | Delete a consumer (`confirm`) |

`GET /events` takes `stream` (default `EVENTS`), `subject` (repeatable, wildcards), `dir` (`backward`, the default, is
newest first; `forward`), `start_seq`, `start_time` (forward only), `limit` (1–500), `type` (exact, or `(untyped)`),
`header` (`key=value`, repeatable), `contains` (payload substring), `json_path` with `json_value`, and `cursor`. The
response has `events[]`, an opaque `next` cursor bound to the filters, `scanned`, `scanned_bytes`, and `partial` with a
`reason` (`msgs`, `bytes`, `time`) when the scan budget ran out before the page filled. At most 4 queries run at once;
more get 429.

### Rollups

| Method | Path | Purpose |
|---|---|---|
| GET | `/overview` | KPIs, top domains, types and subjects, disk forecast, retention horizon, subject index stats |
| GET | `/throughput` | Time series: `range` (`1h`, `24h`, `7d`, `30d`, `1y`), `dim` (`total`, `type`, `p1`–`p4`), `top` |
| GET | `/subjects/tree` | One level of the subject tree below `prefix` |
| GET | `/subjects/info` | One indexed subject with its sampled JSON keys |

### Clients, tokens and audit

| Method | Path | Purpose |
|---|---|---|
| GET, POST | `/clients` | List clients (with live connection counts); create one and return its `.creds` once |
| GET, PUT | `/clients/{id}` | Read; update name, description and tags |
| PUT | `/clients/{id}/grants` | Replace the grants (live; no new creds) |
| POST | `/clients/{id}/rotate` | New creds; the old user key is revoked |
| POST | `/clients/{id}/revoke` | Permanent revoke |
| POST | `/clients/{id}/kick` | Disconnect live connections |
| GET | `/clients/{id}/connections` | Live connections |
| GET, POST | `/tokens` | List HTTP tokens; create one (the secret is returned once) |
| POST | `/tokens/{id}/revoke` | Revoke a token |
| GET | `/audit` | Audit log, newest first: `action` (exact, or a prefix ending in `.`), `actor`, `target`, `result`, `since`, `until`, `before_id`, `limit` |

### Webhooks

| Method | Path | Purpose |
|---|---|---|
| GET, POST | `/hooks/inbound` | List; create an inbound hook (the secret is returned once) |
| PUT, DELETE | `/hooks/inbound/{id}` | Update (`rotate_secret` for a new secret); delete |
| GET, POST | `/hooks/outbound` | List; create an outbound hook |
| GET, PUT, DELETE | `/hooks/outbound/{id}` | Read, update (`rotate_secret`), delete |
| POST | `/hooks/outbound/{id}/pause`, `/resume` | Pause deliveries (keeps the position) or resume them |
| POST | `/hooks/outbound/{id}/test` | Send a test delivery |
| GET | `/hooks/outbound/{id}/deliveries` | Delivery log |
| POST | `/hooks/deliveries/{delivery}/retry` | Retry a delivery |

### Security

| Method | Path | Purpose |
|---|---|---|
| GET | `/security` | Edge status, active bans, history, recent failures, offenders, allowlist |
| POST | `/security/bans` | Ban a network: `{"network", "duration_hours" (0 = no expiry), "reason"}` |
| POST | `/security/bans/lift` | Lift a ban: `{"network"}` |
| POST | `/security/allowlist`, `/security/allowlist/remove` | Add or remove a never-ban network |

### Alerts

| Method | Path | Purpose |
|---|---|---|
| GET | `/alerts` | Rules with live instances, notifier counters, firing count |
| POST | `/alerts/rules` | Create a custom threshold rule |
| PUT, DELETE | `/alerts/rules/{id}` | Update a rule; delete a custom rule (built-ins return 409) |
| POST | `/alerts/evaluate` | Evaluate every rule now |
| GET | `/alerts/history` | Transitions: `rule`, `before_id`, `limit` |
| POST | `/alerts/notifiers/{name}/test` | Send a test notification (502 with the reason on failure) |

### Backups

| Method | Path | Purpose |
|---|---|---|
| GET | `/backups` | Settings, schedule, retention, recipients, running operation, catalog, last restore |
| POST | `/backups/run` | Back up now (202; 409 while another operation runs) |
| POST | `/backups/prune` | Apply retention now |
| GET | `/backups/remote` | Complete backups in the object store, including other servers' |
| POST | `/backups/{id}/plan` | Dry run of a restore |
| POST | `/backups/{id}/restore` | Restore in the background (202; `replace` needs `confirm=<id>`) |

Plan and restore take `{"identity": "AGE-SECRET-KEY-1…", "streams": [...], "replace": bool}`. The identity is used
for that request only. A wrong identity, or an object that fails its integrity check, returns 422.

### KV and objects

| Method | Path | Purpose |
|---|---|---|
| GET, POST | `/kv` | List KV and object buckets; create a KV bucket |
| DELETE | `/kv/{bucket}` | Delete a KV bucket (`confirm`) |
| GET | `/kv/{bucket}/keys` | Keys with their latest revision (`filter`, `limit`; `truncated` when more exist) |
| GET, PUT, DELETE | `/kv/{bucket}/entry?key=` | A value (utf8 or base64) and its history; set; delete (`purge=true` drops history) |
| POST | `/objects` | Create an object store |
| GET, DELETE | `/objects/{bucket}` | List objects; delete the store (`confirm`) |
| GET | `/objects/{bucket}/download?name=` | Download (attachment, `nosniff`) |
| PUT | `/objects/{bucket}/upload?name=` | Upload a raw body up to 64 MiB |
| DELETE | `/objects/{bucket}/object?name=` | Delete an object |

The managed `blobs` store is read-only here (409).
