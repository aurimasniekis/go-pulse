# Admin API

The dashboard is a client of the admin API, and everything it does is available to scripts through the SSH tunnel.
The full OpenAPI 3.1 description is generated from the code:
[`api/admin.openapi.json`](https://github.com/aurimasniekis/go-pulse/blob/main/api/admin.openapi.json). The public
API that tools use is described in [HTTP API](/clients/http)
([`api/public.openapi.json`](https://github.com/aurimasniekis/go-pulse/blob/main/api/public.openapi.json)).

## Listeners and authentication

| | |
|---|---|
| Listeners | The loopback admin listener (`PULSE_ADMIN_ADDR`, default `127.0.0.1:8080`, published on host loopback only, reached with `ssh -L 8080:127.0.0.1:8080 <vps>`); with `PULSE_ADMIN_PUBLIC=true` also the dashboard host on the public listener (behind Cloudflare and AOP) |
| Base path | `/api/admin/v1` (the dashboard is served at `/`; SSO redirects use `/auth/oidc/…`) |
| Authentication | A signed-in user's session cookie, or a personal access token (loopback listener only). See [Users and single sign-on](/operators/users) |

Every operation needs a permission that the user's roles grant (administrator, operator, auditor, viewer); otherwise
**403**. Without a session the answer is **401**.

**Browser sessions.** `POST /auth/login` with `{"email","password"}` sets the session cookie (`__Host-pulse_session` on
https, `pulse_session` on the plain-http loopback listener) and returns `{mfa_pending, must_enroll, me}`; finish with
`POST /auth/mfa` (or TOTP enrollment). `GET /auth/me` returns the user, their permissions and the session's
`csrf_token`. Every `POST`, `PUT`, `PATCH` and `DELETE` with a session must send:

- `X-Pulse-CSRF: <csrf_token>` (the session's token; without it or with a wrong one: 403), and
- an `Origin` equal to the dashboard's own origin (browsers send it; cross-site requests get 403).

**Personal access tokens** for scripts and the CLI: `Authorization: Bearer pulse_pat_…`. Accepted only on the loopback
listener (401 on the public dashboard), no CSRF token or Origin needed, limited to the token's role while the user
still holds it.

**Errors** carry a stable `code` next to the RFC 9457 fields:

| `code` | Status | Meaning |
|---|---|---|
| `unauthorized` | 401 | No valid session or token; sign in |
| `reauth_required` | 401 | The action needs a sign-in from the last `PULSE_REAUTH_WINDOW` (5 min): `POST /auth/reauth` or an SSO step-up, then retry |
| `mfa_required` | 401 | Same, with a second factor (or the session still waits for its TOTP code) |
| `forbidden` | 403 | Missing permission, CSRF token or same origin |
| `invalid`, `not_found`, `conflict`, `precondition`, `rate_limited`, `unavailable` | 422, 404, 409, 412, 429, 503 | As named |

Other protections:

- **Host allowlist** on the loopback listener: only `127.0.0.1`, `localhost`, `::1` (any port) and `PULSE_ADMIN_HOSTS`;
  anything else gets **421** (DNS rebinding). The public listener routes only the configured dashboard host.
- **No CORS**, a strict Content-Security-Policy and `X-Frame-Options: DENY` on every response; HSTS on https.
- **Confirmation.** Destructive calls take `?confirm=<name>` that repeats the target's name: deleting a stream, a
  consumer, a KV bucket or an object store, and a restore with `replace` (the backup id).

Every change is written to the audit log with the user (and the token, for token calls) as actor.

```sh
export PULSE_ADMIN_TOKEN=pulse_pat_…   # Account → Personal access tokens
curl -s -H "Authorization: Bearer $PULSE_ADMIN_TOKEN" http://127.0.0.1:8080/api/admin/v1/system/info | jq .version
curl -s -X POST -H "Authorization: Bearer $PULSE_ADMIN_TOKEN" http://127.0.0.1:8080/api/admin/v1/backups/run
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
| GET | `/system/info` | Version, profile, NATS connection and server stats, JetStream usage, pulsed runtime, connect info |
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

### Sign-in and account

| Method | Path | Purpose |
|---|---|---|
| GET | `/auth/config` | Sign-in options: password sign-in on or off, SSO providers, whether SSO runs on this origin, whether the first administrator is still missing (public) |
| POST | `/auth/login` | Email and password; sets the session cookie (public) |
| POST | `/auth/mfa` | Finish signing in with a TOTP or recovery code |
| POST | `/auth/logout` | End the session; may return the provider's logout URL |
| GET | `/auth/me` | The user, permissions, session and CSRF token |
| POST | `/auth/reauth`, `/auth/reauth/totp` | Confirm it is you (password and code, or TOTP for SSO sessions whose provider allows it) |
| POST | `/auth/setup`, `/auth/recover` | Redeem the setup token (`pulsed admin bootstrap`) or a break-glass token (`pulsed admin recover`) (public) |
| POST | `/auth/invitations/{token}/accept`, `/auth/reset/{token}` | Accept an invitation, set a password with a password link (public) |
| GET, DELETE | `/account/sessions`, `/account/sessions/{id}` | Your sessions; end one |
| POST | `/account/password` | Change your password |
| POST, DELETE | `/account/totp/begin`, `/account/totp/confirm`, `/account/totp` | Enroll, replace or remove TOTP |
| POST | `/account/recovery-codes` | New recovery codes (shown once) |
| GET | `/account/identities` | Your linked SSO identities |
| GET, POST, DELETE | `/account/tokens`, `/account/tokens/{id}` | Your personal access tokens (the secret is returned once) |

SSO itself is a browser redirect: `GET /auth/oidc/{provider}/start?purpose=login|stepup|test|link&return_to=…` and
`GET /auth/oidc/callback`.

### Users and single sign-on

| Method | Path | Purpose |
|---|---|---|
| GET, POST | `/users` | List users; create one (no password: send a password link or link an SSO identity) |
| GET, PATCH, DELETE | `/users/{id}` | Read; change name, roles, status or MFA requirement; delete |
| POST | `/users/{id}/reset-link` | Single-use password link (1 h, shown once) |
| GET, DELETE | `/users/{id}/sessions` | The user's sessions; sign them out everywhere |
| POST | `/users/{id}/clear-throttle` | Clear sign-in throttling |
| GET, DELETE | `/users/{id}/tokens`, `/users/{id}/tokens/{token}` | The user's personal access tokens; revoke one |
| GET, POST, DELETE | `/users/{id}/identities` | Linked SSO identities; link one (provider and subject); unlink (`issuer`, `subject`) |
| POST | `/users/{id}/link-invite` | Single-use link that binds the next SSO login to this user (72 h) |
| GET, POST, DELETE | `/invitations`, `/invitations/{id}` | Pending invitations; invite by email with roles (link shown once); revoke |
| GET, POST | `/sso/providers` | SSO providers (with the redirect URI to register); add one |
| GET, PUT, DELETE | `/sso/providers/{id}` | Read; change (with `version`; the client secret is write-only); remove |
| GET, POST, DELETE | `/sso/providers/{id}/mappings`, `/sso/providers/{id}/mappings/{mapping}` | Group → role mappings |
| GET | `/sso/providers/{id}/test-result` | The result of your latest test login with that provider |
| GET, PUT | `/sso/settings` | Password sign-in on or off (SSO-only) |
