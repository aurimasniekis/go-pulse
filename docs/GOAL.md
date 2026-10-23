# Pulse — Product Contract (GOAL)

> **Status:** canonical product contract. Established at M0 on 2026-10-23.
> Changes to this file are contract changes: record them in `docs/DECISIONS.md` with a justification.
> Security requirements may never be weakened silently (see `AGENTS.md`).
> Execution plan: `docs/PLAN.md`. Decisions: `docs/DECISIONS.md`. Threats: `docs/THREAT_MODEL.md`.

## 1. Product statement

Pulse is a **personal, durable, secure event bus**. Every personal tool, bot, agent and service publishes and
consumes events through it. A new tool can **replay one to two years of history from a timestamp**.

```
tools / bots / agents ──NATS TLS (4222, .creds)──▶ ┌─────────────── VPS ───────────────┐
browsers ──HTTPS via Cloudflare──▶ :443 pulsed ──▶ │ NATS 2.15 JetStream (EVENTS, blobs) │
webhook senders ──HTTPS via Cloudflare──▶ /hooks ─▶ │ pulsed + SQLite: API, UI, rollups…  │
operator ──ssh -L──▶ 127.0.0.1:8080 admin UI ─────▶ └───────────────────────────────────┘
```

The product is the `pulsed` server (NATS bootstrap, API, embedded dashboard, rollups, edge TLS, bans, webhooks,
backups, alerts), the `pulse` CLI, and SDKs for Go, Python and TypeScript. It ships as one distroless container image
plus the official NATS image. Events live in NATS JetStream. pulsed's own state (clients, tokens, webhooks, bans, audit,
alerts, rollups, subject index, settings, backup catalog) lives in a local SQLite database owned by pulsed.

## 2. Scope

In scope for v1.0:

1. NATS 2.15 bootstrap in JWT operator mode with a full resolver: operator, SYS/APP accounts, internal users.
2. The durable `EVENTS` stream (`evt.>`), a subject convention, a CloudEvents-lite envelope and user-defined extra streams.
3. Client identity: per-client NATS user JWTs minted from least-privilege grants, `.creds` shown once, rotation, revocation.
4. HTTP ingress: token publish, a request/reply bridge, named inbound webhooks with verifiers, outbound webhooks with
   retries, HMAC and a delivery log, HTTP replay and an SSE tail, and a WebSocket proxy for browser NATS clients.
5. An operator dashboard (Go + React, embedded) with an OpenAPI 3.1 admin API and SSE live data.
6. Rollups (counts per minute, hour and day), a subject index, a disk forecast and a retention horizon, stored in SQLite.
7. An event explorer (forward and backward paging, filters, budgets) and a live tail.
8. Edge security: TLS on 443 with a Cloudflare Origin CA cert and Authenticated Origin Pulls (mTLS), Let's Encrypt for
   raw NATS via ACME DNS-01, fail2ban-style automatic bans with nftables, HTTP and Cloudflare enforcement.
9. Observability: slog JSON logs, Prometheus metrics on a separate loopback port, alert rules, Telegram and Discord.
10. Backups: nightly, age-encrypted, to S3-compatible storage, on a GFS schedule, with restore from the dashboard and CLI.
11. SDKs (Go, Python, TypeScript for node and browser) and the `pulse` CLI.
12. Packaging: multi-arch distroless image on GHCR, CI with security scanning, SBOM and provenance, a dev compose stack,
    and a documented env/secret contract for the deployment repository.

## 3. Non-goals (v1.0)

- **No high availability or clustering.** One NATS server and one pulsed on one VPS. Recovery is restore from backup.
- **No public dashboard** until central SSO (OIDC) lands. The admin UI is reachable only through an SSH tunnel.
- **No database server.** pulsed state is one local SQLite file (pure Go driver, WAL). No Postgres, no shared or network-mounted
  database. Events are never copied into SQLite.
- **No reverse proxy** in front of pulsed. pulsed terminates TLS itself.
- **No exactly-once delivery** beyond JetStream's 2-minute de-duplication window on `Nats-Msg-Id`.
- **No schema registry.** `ce-type` is a label, not a validated schema.
- **No multi-tenant isolation** between tool clients beyond NATS permissions. Pulse is a single-owner system.
- **No payload encryption at rest** inside NATS. Disk encryption is the host's responsibility. Backups are encrypted.
- **No remote_write metrics shipping** in v1 (Alloy has a commented placeholder).

## 4. Security boundaries and guarantees

### 4.1 Guarantees (when deployed per the operator guide)

| # | Guarantee | Requirement IDs |
|---|---|---|
| G-1 | A tool client can publish or subscribe only on subjects its grants allow. A new client has no permissions. | PL-AUTH-003..006 |
| G-2 | A revoked client is disconnected within 5 s and cannot reconnect with the same credentials. | PL-AUTH-007 |
| G-3 | Client seeds are shown once and never stored by pulsed. | PL-AUTH-004 |
| G-4 | The admin UI and admin API are never reachable from the network while `auth.mode=none`. | PL-ADM-001..003 |
| G-5 | Public HTTP routes accept only connections that passed Cloudflare Authenticated Origin Pulls in prod mode. | PL-EDGE-002..003 |
| G-6 | The real client IP comes from `CF-Connecting-IP` only when the peer is in Cloudflare ranges and passed mTLS. | PL-EDGE-004 |
| G-7 | Inbound webhooks are accepted only with a valid signature or secret for that hook. | PL-HOOK-002..004 |
| G-8 | Outbound webhooks cannot reach loopback, private, link-local or metadata addresses. | PL-HOOK-008 |
| G-9 | Signing keys at rest are sealed with a master key that never touches the data volume. | PL-KEY-001..003 |
| G-10 | Backups are encrypted before they leave the host. | PL-BAK-002 |
| G-11 | Repeated authentication failures from one IP lead to a ban enforced at the HTTP layer and in nftables. | PL-BAN-001..006 |

### 4.2 What Pulse does not guarantee

- **pulsed is a high-value component.** Whoever controls pulsed and holds the master key can mint any credential and read every event.
- **Payloads are plaintext inside NATS.** Anyone with root on the VPS can read events.
- **Cloudflare is trusted** to terminate TLS for public HTTP routes, see plaintext requests and set `CF-Connecting-IP` honestly.
- **Bans are a nuisance filter, not a DDoS defense.** They are bounded by nftables set sizes and Cloudflare free-plan limits.
- **Grants are subject based.** A client allowed to read `evt.a.>` reads every event on those subjects, payloads included.
- **De-duplication is bounded** to the stream's 2-minute window.
- **Durable consumer names are not namespaced** by NATS permissions. See THREAT_MODEL.md T-12.

### 4.3 Trust boundaries (summary; full model in `docs/THREAT_MODEL.md`)

TB-1 tool client ↔ NATS 4222 (TLS + user JWT) · TB-2 Cloudflare ↔ pulsed :443 (AOP mTLS) · TB-3 webhook sender ↔ /hooks
(signatures) · TB-4 operator ↔ admin UI (SSH tunnel; future OIDC) · TB-5 pulsed ↔ NATS (internal creds, TLS) · TB-6 pulsed ↔
outbound webhook targets (SSRF guard) · TB-7 pulsed ↔ S3 (age-encrypted) · TB-8 pulsed ↔ Cloudflare/ACME APIs (scoped tokens) ·
TB-9 host root ↔ data volume (SQLite, NATS store) and master key · TB-10 banagent ↔ pulsed loopback ban endpoint (bearer token).

## 5. Key definitions

| Term | Meaning |
|---|---|
| Event | A NATS message on a subject under `evt.>` stored in the `EVENTS` stream. |
| Envelope | CloudEvents-lite headers: `Nats-Msg-Id`, `ce-type`, `ce-source`, `ce-time`, `content-type`. |
| Untyped event | An event without `ce-type`, e.g. from raw `nats pub`. Accepted and shown as "untyped". |
| Client | A tool identity in the APP account: one NATS user JWT plus grants, metadata and status. |
| Grant | A permission statement on a client: action (`publish`, `subscribe`, `request`, `reply`, `durable`, `blobs`) × subject pattern. |
| HTTP token | A bearer token for the public HTTP API, scoped by grants like a client. |
| Inbound hook | A named endpoint `/hooks/<name>` with a verifier that publishes verified requests to a subject. |
| Outbound hook | A subscription (subject filter → URL) that delivers events with retries, HMAC signing and a delivery log. |
| Rollup | Per-minute, per-hour and per-day counts by subject prefix and `ce-type`, stored in SQLite. |
| Retention horizon | `max_bytes` ÷ average daily bytes: how many days of history fit in the stream at the current rate. |
| Ban | A SQLite record with an expiry that blocks an IP at nftables, HTTP and optionally Cloudflare. |
| Master key | A 32-byte secret, supplied as a compose secret, that seals `keys.enc`. |

## 6. Canonical requirements

Requirement IDs are stable: `PL-<AREA>-NNN`. They are never reused or renumbered. A dropped requirement is marked
**Withdrawn** with a DECISIONS.md reference, never deleted. Total requirements at M0: 162.

### PL-BUS — Streams and subjects

| ID | Requirement |
|---|---|
| PL-BUS-001 | **EVENTS stream.** pulsed ensures stream `EVENTS` on `evt.>` in the APP account: file storage, unlimited age and messages, `max_bytes` from config (deployment computes 60% of disk), discard old, S2 compression, 2-minute duplicate window, `allow_direct`. Drift is corrected on start, except for `max_bytes` decreases, which require explicit confirmation. |
| PL-BUS-002 | **Subject grammar.** Event subjects are `evt.<domain>.<entity>.<action>[.<key>…]`: at least 4 tokens, each matching `[a-z0-9_-]+`. Gateway, SDKs and CLI reject other subjects before publishing. |
| PL-BUS-003 | **Raw publishes accepted.** Messages published directly with NATS tools to any subject under `evt.>` are stored and shown, marked untyped when they lack `ce-type`. |
| PL-BUS-004 | **Payload size.** NATS `max_payload` is 1 MB. Larger blobs are stored in the `blobs` Object Store and the event carries a `pulse-blob` reference header. |
| PL-BUS-005 | **Extra streams.** The operator can create, edit and delete additional streams in the APP account from the dashboard, with validation that their subjects do not overlap `evt.>` or `_sys.>`. `EVENTS` cannot be deleted from the UI. |
| PL-BUS-006 | **Consumers.** The dashboard lists consumers per stream with pending, ack-pending and redelivery counts, and can delete a consumer with confirmation. |
| PL-BUS-007 | **Replay by time.** Any client with a subscribe grant can replay from a timestamp (`DeliverByStartTime`) or a sequence. |
| PL-BUS-008 | **Internal state isolation.** pulsed's internal state (clients, tokens, hooks, audit, delivery logs, bans, rollups) lives in pulsed's SQLite database, which is not reachable through NATS. |
| PL-BUS-009 | **Subject cardinality guidance.** `docs/ENVELOPE.md` documents a subject-cardinality guideline measured in the M2 spike. |

### PL-ENV — Envelope

| ID | Requirement |
|---|---|
| PL-ENV-001 | **Headers.** Typed events carry `Nats-Msg-Id` (ULID), `ce-type`, `ce-source`, `ce-time` (RFC 3339 with nanoseconds, UTC) and `content-type`. `ce-specversion` is implied `1.0`. |
| PL-ENV-002 | **Shared encoder.** `pkg/pulse/envelope` is the single Go implementation. Python and TS SDKs pass the same conformance vectors in `sdk/conformance`. |
| PL-ENV-003 | **Idempotency.** Publishers set `Nats-Msg-Id`. A retry with the same ID within 2 minutes is stored once. |
| PL-ENV-004 | **Blob references.** `pulse-blob: <bucket>/<object>` plus `pulse-blob-size` and `pulse-blob-sha256` headers point to Object Store data. |
| PL-ENV-005 | **Documentation.** `docs/ENVELOPE.md` specifies headers, content types, the subject grammar and examples in every SDK. |

### PL-KEY — Key management and bootstrap

| ID | Requirement |
|---|---|
| PL-KEY-001 | **Sealed key store.** All nkeys seeds (operator, signing keys, accounts, internal users), the internal CA key and the ACME account key are stored in `keys.enc`, sealed with XChaCha20-Poly1305 under a key derived from the master key, with a versioned header bound as AAD. |
| PL-KEY-002 | **Master key handling.** The master key comes from `PULSE_MASTER_KEY_FILE` (or `PULSE_MASTER_KEY` for dev only). It must be 32 random bytes (hex or base64). It is never written to the data volume, and production refuses to start without it. A wrong key fails closed with a clear error. |
| PL-KEY-003 | **Idempotent init.** `pulsed init` creates missing keys and artifacts, and re-renders derived files. It never rotates existing keys implicitly. Running it twice changes nothing. |
| PL-KEY-004 | **Bootstrap artifacts.** init writes `operator.jwt`, renders `nats.conf` (SYS preloaded, full resolver), issues the bootstrap TLS leaf for 4222 from the internal CA, and generates the banagent token (`banagent.token`). |
| PL-KEY-005 | **Account sync.** On start, `pulsed serve` pushes the APP account JWT with `$SYS.REQ.CLAIMS.UPDATE` when they are missing or stale. |
| PL-KEY-006 | **Signing keys.** Account JWTs are signed by an operator signing key, and user JWTs by account signing keys, never by identity keys. |
| PL-KEY-007 | **Key export.** `pulsed keys export` writes the decrypted key set to stdout only with an explicit `--yes-i-understand` flag and an audit record. |

### PL-AUTH — Tool client identity and permissions

| ID | Requirement |
|---|---|
| PL-AUTH-001 | **Operator mode.** NATS runs in JWT operator mode with the full resolver. Accounts: SYS and APP (tool clients and pulsed's internal APP user). |
| PL-AUTH-002 | **Client records.** A client has an ID, name, description, tags, NATS user public key, grants, expiry (default 365 d), status (`active`, `revoked`, `expired`) and timestamps. |
| PL-AUTH-003 | **Least privilege.** A new client JWT allows nothing except its private inbox until grants are added. |
| PL-AUTH-004 | **Credentials once.** Creating or rotating a client returns the `.creds` exactly once. pulsed does not store the user seed. |
| PL-AUTH-005 | **Grants to permissions.** A deterministic generator turns grants into NATS permissions: publish subjects, subscribe subjects, a per-client inbox `_INBOX_<id>.>`, filter-scoped `$JS.API.CONSUMER.CREATE.<stream>.*.<filter>`, consumer info and next on the stream, and JS acks. |
| PL-AUTH-006 | **Explicit denies.** Every client JWT denies stream administration, unfiltered direct get and message get, `$KV.>`, `$SYS.>`, and `$O.>` unless the client has a `blobs` grant. |
| PL-AUTH-007 | **Revocation.** Revoking a client adds it to the APP account's revocation list, re-pushes the account JWT and disconnects the client within 5 s. |
| PL-AUTH-008 | **Rotation.** Rotating credentials mints a new user key and JWT and revokes the previous one. |
| PL-AUTH-009 | **Grant edits.** Editing grants updates the client's permissions without issuing new credentials, by rewriting the client's scoped signing key in the APP account JWT. The effect on already connected clients is tested and shown in the UI; the dashboard can kick the client to force a reconnect. |
| PL-AUTH-010 | **Connection view.** The dashboard shows live connections per client (IP, RTT, subs, in/out msgs and bytes, uptime, version) from `CONNZ`. |
| PL-AUTH-011 | **Snippets.** The client page shows ready-to-use snippets for Go, Python, TS, curl and the `nats` CLI. |
| PL-AUTH-012 | **Expiry warnings.** Clients expiring in under 30 days raise a warning and an alert event. |

### PL-ADM — Dashboard access

| ID | Requirement |
|---|---|
| PL-ADM-001 | **Loopback admin listener.** The admin UI and `/api/admin/v1` are served only on the admin listener (default `127.0.0.1:8080`). |
| PL-ADM-002 | **Authenticator seam.** Admin requests pass an `Authenticator` interface. Modes: `none` (implemented) and `oidc` (stub that refuses to start until implemented). |
| PL-ADM-003 | **Refuse unsafe exposure.** With `PULSE_PROFILE=prod`, pulsed refuses to start if `PULSE_ADMIN_PUBLIC=true` while `auth.mode=none`, or if the admin listener is not a loopback address while `auth.mode=none`. |
| PL-ADM-004 | **Browser hardening.** Admin responses set a strict CSP, `X-Content-Type-Options`, `Referrer-Policy`, frame denial. State-changing admin requests require a custom `X-Pulse-CSRF` header. |
| PL-ADM-005 | **Future OIDC contract.** `docs/operator/sso.md` specifies the redirect-to-login flow, session cookie and CSRF requirements the `oidc` mode must meet. |

### PL-EDGE — Public listener and TLS

| ID | Requirement |
|---|---|
| PL-EDGE-001 | **Public routes only.** The public listener (`:8443`, published as 443) serves only `/api/v1/*`, `/hooks/*`, `/ws` and `/healthz`. All other paths return 404. |
| PL-EDGE-002 | **Origin certificate.** In prod mode the public listener serves the Cloudflare Origin CA certificate from files, and reloads it when the file mtime changes. |
| PL-EDGE-003 | **Authenticated Origin Pulls.** In prod mode the public listener requires a client certificate that chains to the configured AOP client CA (the Terraform module's own CA). Connections without it fail the TLS handshake. |
| PL-EDGE-004 | **Real client IP.** `CF-Connecting-IP` is trusted only when the TCP peer is in the Cloudflare IP ranges **and** the connection passed AOP mTLS. Otherwise the peer address is used. |
| PL-EDGE-005 | **WebSocket proxy.** `/ws` proxies to the NATS websocket port using `httputil.ReverseProxy` with `Rewrite`, setting a single `X-Forwarded-For` with the real client IP. |
| PL-EDGE-006 | **Raw NATS TLS.** Port 4222 uses TLS. pulsed obtains a Let's Encrypt certificate for the raw hostname via ACME DNS-01 (lego, Cloudflare provider), renews it 30 days before expiry, writes files atomically, reloads NATS via `$SYS.REQ.SERVER.<id>.RELOAD` and verifies the served serial. |
| PL-EDGE-007 | **Bootstrap TLS.** Until ACME succeeds, 4222 serves a leaf from the internal CA. |
| PL-EDGE-008 | **Cloudflare ranges.** pulsed ships a CF IPv4/IPv6 range list, refreshes it daily from Cloudflare's API when enabled, and exposes it for the firewall seed script. |
| PL-EDGE-009 | **Timeouts and limits.** Public HTTP has read-header 10 s, idle 120 s, body limits (1 MB publish, 5 MB hooks) and a per-IP token bucket. |
| PL-EDGE-010 | **Dev mode.** In dev mode the public listener may serve plain HTTP on a configurable address. Prod refuses plain HTTP. |

### PL-BAN — Automatic bans

| ID | Requirement |
|---|---|
| PL-BAN-001 | **Detection sources.** NATS `CLIENT.AUTH.ERR` advisories (`client.host`), HTTP 401 on token routes, and bad webhook signatures feed a detector. |
| PL-BAN-002 | **Sliding windows.** A configurable threshold (default 5 failures in 10 min) bans an IP for a base duration (default 1 h). |
| PL-BAN-003 | **Escalation.** Repeat offenders get ×4 per offence, up to 30 days. Offence history is kept for 30 days. |
| PL-BAN-004 | **Never-ban list.** Loopback, RFC 1918/ULA private ranges, Docker ranges, the host's own addresses, Cloudflare ranges and an operator allowlist are never banned. |
| PL-BAN-005 | **Storage.** Bans live in SQLite with an expiry time and are pruned when expired. Manual bans and unbans from the dashboard and API are audited. |
| PL-BAN-006 | **HTTP enforcement.** A middleware rejects banned real IPs on the public listener with 403. |
| PL-BAN-007 | **nftables enforcement.** `pulsed banagent` polls pulsed's loopback ban endpoint (bearer token from `banagent.token`) and reconciles the `pulse_ban_v4`/`pulse_ban_v6` sets in an Ansible-owned table at `prerouting priority raw`, with element timeouts. It never touches other tables. |
| PL-BAN-008 | **Cloudflare list (optional).** Bans of at least 1 h are synced to a Cloudflare IP list, with removals on expiry. |
| PL-BAN-009 | **Security page.** The dashboard shows active bans, recent failures, offenders, allowlist, and lets the operator ban, unban and allowlist. |

### PL-ING — HTTP ingress (token API)

| ID | Requirement |
|---|---|
| PL-ING-001 | **HTTP tokens.** The operator creates bearer tokens with grants, expiry and optional source CIDRs. The secret is shown once and stored as a SHA-256 hash with a prefix for identification. |
| PL-ING-002 | **Publish.** `POST /api/v1/publish/{subject}` publishes the body with envelope headers derived from request headers and returns the stream sequence. It honors `Idempotency-Key` as `Nats-Msg-Id`. |
| PL-ING-003 | **Batch publish.** `POST /api/v1/publish` accepts up to 100 events in one JSON request. |
| PL-ING-004 | **Request bridge.** `POST /api/v1/request/{subject}` does a NATS request with a bounded timeout (max 30 s) and returns the reply. |
| PL-ING-005 | **HTTP replay.** `GET /api/v1/events?subject=&since=&seq=&limit=` returns events in pages with an opaque cursor. |
| PL-ING-006 | **SSE tail.** `GET /api/v1/tail?subject=&since=` streams events as SSE with keepalives, honoring `Last-Event-ID`. |
| PL-ING-007 | **Authorization.** Every token route checks the token grants against the subject. Failures return 403. A bad or missing token returns 401 and feeds the ban detector. |
| PL-ING-008 | **Audit.** Token creation, revocation and admin actions are written to the SQLite audit log (PL-AUD-001). |

### PL-HOOK — Webhooks

| ID | Requirement |
|---|---|
| PL-HOOK-001 | **Named inbound hooks.** `POST /hooks/{name}` publishes verified requests to the hook's subject (default `evt.hook.<name>.received`) with headers mapped into the envelope. |
| PL-HOOK-002 | **Verifiers.** GitHub (`X-Hub-Signature-256`), Stripe (`Stripe-Signature` with timestamp tolerance), generic HMAC-SHA256 (configurable header, hex or base64, optional timestamp) and shared secret (constant-time compare). |
| PL-HOOK-003 | **Verifier tests.** The GitHub and Stripe verifiers pass the vectors published in their documentation. |
| PL-HOOK-004 | **Failure handling.** Invalid signatures return 401 without detail and feed the ban detector. Replays outside the tolerance window are rejected. |
| PL-HOOK-005 | **Outbound hooks.** An outbound hook maps a subject filter to an HTTPS URL. A durable consumer per hook delivers events in order of arrival with a bounded parallelism of 1. |
| PL-HOOK-006 | **Retries.** Failed deliveries retry with exponential backoff (1 s … 1 h, max 12 attempts) using JetStream redelivery. Exhausted deliveries are logged as dead letters. |
| PL-HOOK-007 | **Signing.** Outbound requests carry `Pulse-Signature: t=<unix>,v1=<hex hmac-sha256(secret, t + "." + body)>` and the envelope as headers. |
| PL-HOOK-008 | **SSRF guard.** Outbound requests use a dialer that refuses loopback, private, link-local, CGNAT, multicast and metadata addresses after DNS resolution, unless the host is in an explicit allowlist. |
| PL-HOOK-009 | **Delivery log.** Every attempt is recorded (status, latency, response excerpt ≤ 2 KB) in SQLite with a 30-day retention. The dashboard shows it and can retry a delivery. |
| PL-HOOK-010 | **Pause/resume.** Outbound hooks can be paused and resumed without losing position. |

### PL-API — API

| ID | Requirement |
|---|---|
| PL-API-001 | **Huma v2 on net/http.** All HTTP APIs are defined with Huma, producing an OpenAPI 3.1 document. |
| PL-API-002 | **Spec export.** `pulsed openapi` prints the admin and public specs. The UI types are generated from them with `openapi-typescript`, and CI fails on drift. |
| PL-API-003 | **Errors.** Errors use RFC 9457 problem details. Internal errors never leak internals. |
| PL-API-004 | **Live data.** Live views use SSE with keepalives within Huma SSE's write deadline. |
| PL-API-005 | **Versioning.** Public routes are under `/api/v1`, admin under `/api/admin/v1`. Breaking changes need a new version. |

### PL-EXP — Explorer and tail

| ID | Requirement |
|---|---|
| PL-EXP-001 | **Forward reads.** Single-filter forward reads use batch direct get by sequence or start time. |
| PL-EXP-002 | **Multi-filter reads.** Reads with several subject filters use an ordered consumer. |
| PL-EXP-003 | **Newest first.** Newest-first paging uses backward windowing over sequences. |
| PL-EXP-004 | **Opaque cursor.** Pages carry an opaque cursor encoding stream, direction, sequence and filter hash. A cursor used with different filters is rejected. |
| PL-EXP-005 | **Server-side filters.** Header, `ce-type` and payload substring/JSON-path filters run in pulsed. |
| PL-EXP-006 | **Budgets.** One query scans at most 50k messages, 64 MB or 3 s, and at most 4 queries run at once. Exhausted budgets return partial results with a resume cursor. |
| PL-EXP-007 | **Live tail.** Each SSE tail session uses an ordered `DeliverNew` consumer. At most 10 sessions, 200 msg/s each with a sampling notice, keepalive every 20 s. |
| PL-EXP-008 | **Message view.** The explorer shows headers, payload (JSON pretty, text, hex), size, sequence, stream time and envelope fields, and copies a `pulse sub` command for replay from that point. |

### PL-ROLL — Rollups and subject index

| ID | Requirement |
|---|---|
| PL-ROLL-001 | **Headers-only counting.** Per stream, an ordered `HeadersOnly` consumer resumes from a checkpoint in SQLite and counts messages and bytes (from `Nats-Msg-Size`). |
| PL-ROLL-002 | **Dimensions.** Counts are aggregated per minute by stream timestamp: totals, subject prefixes of depth 1–4, and `ce-type`. |
| PL-ROLL-003 | **Storage.** `rollup_1m` (pruned after 8 d), `rollup_1h` and `rollup_1d` SQLite tables are upserted in the same transaction as the checkpoint, so counts survive restarts without double counting. The last 48 h of minute buckets are also kept in RAM. |
| PL-ROLL-004 | **Subject index.** The SQLite `subjects` table holds per subject: first/last seen, last sequence, last type, total count and bytes, and sampled JSON keys (at most every 10 min). It is capped at 50k subjects; overflow is counted. |
| PL-ROLL-005 | **Forecast.** The dashboard shows disk usage, a forecast of when `max_bytes` is reached, and the retention horizon. |
| PL-ROLL-006 | **Overview.** The Overview page shows KPIs, throughput for 1h/24h/30d/1y, top subjects and types, and the forecast. |
| PL-ROLL-007 | **Subject tree.** The Subject tree page browses the index hierarchically with counts and last seen. |

### PL-OBS — Observability and alerts

| ID | Requirement |
|---|---|
| PL-OBS-001 | **Logs.** slog JSON to stdout with level from config. No secrets in logs. |
| PL-OBS-002 | **Metrics.** Prometheus `/metrics` on a separate loopback listener (default `127.0.0.1:9464`) with pulsed metrics plus NATS varz/jsz gauges. |
| PL-OBS-003 | **Health.** `/healthz` (process) and `/readyz` (NATS connected, accounts synced, streams ensured) on the metrics listener. `pulsed healthcheck` probes `/readyz` for container health. |
| PL-OBS-004 | **Alert rules.** Built-in rules (disk forecast < 30 d, stream > 85% of max_bytes, cert expiry < 14 d, backup failed or stale > 36 h, client expiry < 30 d, ban storm, NATS slow consumers, outbound hook dead letters) plus custom threshold rules on rollups. |
| PL-OBS-005 | **Alert events.** Firing and resolved alerts are published as `_sys.alert.<rule>` events in the APP account and stored in the SQLite alert history. |
| PL-OBS-006 | **Notifiers.** Telegram and Discord notifiers with rate limits and a test button. Secrets come from files. |
| PL-OBS-007 | **System page.** The dashboard shows NATS server info, JetStream usage, connections, certificates and their expiry, versions, and pulsed runtime stats. |
| PL-OBS-008 | **Alloy.** A reference Alloy config scrapes pulsed metrics and host metrics; remote_write is a commented placeholder. |

### PL-BAK — Backups

| ID | Requirement |
|---|---|
| PL-BAK-001 | **Schedule.** Nightly backups at a configurable time. |
| PL-BAK-002 | **Encryption.** Every backup object is encrypted with age to configured recipients before upload. The identity is not stored on the server. |
| PL-BAK-003 | **SQLite and small streams.** The SQLite database is snapshotted online with `VACUUM INTO`. Non-EVENTS streams and KV/Object Store buckets get full snapshots via the JetStream snapshot API. |
| PL-BAK-004 | **EVENTS segments.** EVENTS is backed up as append-only daily segments (NATSARC1 format: length-prefixed records with subject, headers, data, sequence and timestamp), plus a weekly full snapshot. |
| PL-BAK-005 | **Retention.** GFS pruning: 7 daily, 4 weekly, 12 monthly. |
| PL-BAK-006 | **Restore.** Restore from the dashboard and the CLI, into the same or a fresh server, with a dry-run listing. |
| PL-BAK-007 | **Timestamp preservation gate.** A restore drill test proves that restored events keep their original timestamps so `DeliverByStartTime` replay works. If segments cannot preserve timestamps, the fallback (full snapshots only) is recorded in DECISIONS.md. |
| PL-BAK-008 | **Catalog.** The dashboard lists backups with size, duration, status and verification result. |
| PL-BAK-009 | **S3 compatibility.** Uploads use minio-go against any S3-compatible service with path or virtual-host style. |

### PL-DB — pulsed state database (SQLite)

| ID | Requirement |
|---|---|
| PL-DB-001 | **Driver and durability.** `modernc.org/sqlite` (pure Go, `CGO_ENABLED=0`), WAL, `synchronous=FULL`, `foreign_keys=ON`, `busy_timeout=5000`, applied on every connection. One writer connection and a small `query_only` reader pool. |
| PL-DB-002 | **Migrations.** Embedded, forward-only SQL migrations run at start under the process lock. A schema newer than the binary refuses to start. |
| PL-DB-003 | **Single owner.** An exclusive `flock` on `<data>/pulsed.lock` ensures one pulsed per data directory. |
| PL-DB-004 | **No network I/O in transactions.** Write transactions never span NATS, HTTP or S3 calls. |
| PL-DB-005 | **Retention.** Periodic pruning: `rollup_1m` > 8 d, delivery log > 30 d, alert history > 90 d, audit > 400 d, expired bans and offences > 30 d. |
| PL-DB-006 | **Permissions and placement.** Data directory 0700, database files 0600, on local storage (network filesystems refused on Linux). |
| PL-DB-007 | **Integrity.** `PRAGMA quick_check` at startup; corruption refuses to start with restore guidance. |

### PL-UI — Dashboard

| ID | Requirement |
|---|---|
| PL-UI-001 | **Stack.** React 19.3, Vite 8, Mantine v8, TanStack Router/Query/Store/Table/Virtual, ECharts, embedded in the binary via `go:embed`. |
| PL-UI-002 | **Look.** Dense observability style: radius 4px, compact rows, Inter + JetBrains Mono, gray + indigo palette, light/dark toggle. |
| PL-UI-003 | **Shell.** Collapsible sidebar, Cmd-K command palette, breadcrumbs, connection status indicator. |
| PL-UI-004 | **Pages.** Overview, Explorer, Live tail, Subjects, Streams & consumers, Clients, HTTP tokens, Webhooks (in/out), Security, KV/Object browser, Backups, Alerts, Audit, System, Settings. |
| PL-UI-005 | **Typed API.** The UI uses `openapi-fetch` with generated types only. No hand-written API types. |
| PL-UI-006 | **Charts.** ECharts themed from Mantine tokens and responsive to the color scheme. |
| PL-UI-007 | **Large lists.** Tables over 200 rows use virtualization. |
| PL-UI-008 | **Secret display.** Secrets (creds, token values, hook secrets) are shown once in a modal with copy and download, and never cached by the query client. |
| PL-UI-009 | **Tests.** vitest for components and Playwright smoke coverage of every page against the dev stack. |
| PL-UI-010 | **Missing UI.** When the UI is not built, pulsed serves a plain notice instead of failing. |

### PL-SDK — SDKs and CLI

| ID | Requirement |
|---|---|
| PL-SDK-001 | **Go SDK.** `pkg/pulse`: `Connect(creds, url)`, `Publish(ctx, subject, type, data, opts)`, `Subscribe(ctx, filter, handler, Since/Seq/Durable)`, `Handle[T]` typed handlers, `Request`, blob put/get. |
| PL-SDK-002 | **Python SDK.** `sdk/python` (async, nats-py) with the same surface, installable via `pip install git+…#subdirectory=sdk/python`. |
| PL-SDK-003 | **TS SDK.** `sdk/ts` with `@nats-io/*` v3 for node and browser (WebSocket via `/ws`), installable from git. |
| PL-SDK-004 | **Conformance.** All SDKs pass `sdk/conformance` vectors for envelope encoding/decoding and subject validation. |
| PL-SDK-005 | **Interop e2e.** Go publishes; Python, TS-node and TS-browser (via `/ws`) replay with `since` and see identical events. |
| PL-SDK-006 | **CLI.** `pulse` with `context` (add/use/list, stored creds paths), `pub`, `sub --since/--seq/--durable`, `req`, `streams`, `tail`, `clients` (admin API via tunnel), `backup`/`restore`. |
| PL-SDK-007 | **Inbox prefix.** SDKs use the per-client inbox prefix `_INBOX_<client-id>` from the creds metadata. |

### PL-OPS — Packaging and operations

| ID | Requirement |
|---|---|
| PL-OPS-001 | **Image.** Multi-stage Dockerfile: node → Go cross-build → `gcr.io/distroless/static-debian13:nonroot`, UID 65532, all base images pinned by digest. |
| PL-OPS-002 | **Commands.** `pulsed serve|init|banagent|healthcheck|openapi|keys export|version`. |
| PL-OPS-003 | **Configuration.** All config via `PULSE_*` env vars, with `*_FILE` variants for secrets. `docs/operator/configuration.md` is the contract with the deployment repo. |
| PL-OPS-004 | **Profiles.** `PULSE_PROFILE=dev|prod`. Prod refuses insecure settings (plain HTTP public listener, missing AOP CA, admin exposure with auth none, dev master key). |
| PL-OPS-005 | **Dev stack.** `deploy/compose.dev.yaml` runs init, nats, pulsed, SeaweedFS (S3) and pebble (ACME). `make dev-up` starts it. |
| PL-OPS-006 | **Reference prod compose.** `deploy/examples/compose.yaml` shows the prod topology (init, nats, pulsed, banagent) with memory limits. |
| PL-OPS-007 | **CI.** golangci-lint, go vet, race tests, govulncheck, gitleaks, web typecheck, vitest and build on every push. All actions pinned by SHA. `GOTOOLCHAIN=local`. |
| PL-OPS-008 | **Release.** `v*` tags build and push a multi-arch image to GHCR with SBOM, Trivy scan and build provenance attestation. |
| PL-OPS-009 | **Memory budget.** NATS runs under 900 MB with `GOMEMLIMIT=600MiB`. pulsed runs under 448 MB with `GOMEMLIMIT=300MiB`. Measured in the M2 spike and recorded in `docs/SIZING.md`. |
| PL-OPS-010 | **Graceful shutdown.** SIGTERM drains HTTP, stops consumers after flushing rollups and checkpoints, and exits within 20 s. |
| PL-OPS-011 | **Versioning.** SemVer per `docs/VERSIONING.md`. `pulsed version` prints version, commit and build date. |

### PL-AUD — Audit

| ID | Requirement |
|---|---|
| PL-AUD-001 | **Audit stream.** Every admin mutation and security event writes an audit record (`actor`, `action`, `target`, `ip`, `result`, `details`) to the SQLite `audit` table, which is append-only for the application and retained for 400 days. |
| PL-AUD-002 | **Audit page.** The dashboard lists audit records with filters (action, actor, target, time). |
| PL-AUD-003 | **No secrets.** Audit records never contain secrets, seeds or token values. |

### PL-TST — Testing

| ID | Requirement |
|---|---|
| PL-TST-001 | **Embedded server tests.** Integration tests boot an embedded nats-server from the generated `nats.conf`. |
| PL-TST-002 | **Race.** All Go tests pass with `-race`. |
| PL-TST-003 | **Bootstrap tests.** creds connect, AUTH.ERR host, revoke → disconnect, accounts persist over restart, RELOAD with a new cert. |
| PL-TST-004 | **Query tests.** Table-driven tests for explorer queries and budgets. |
| PL-TST-005 | **E2E.** Playwright against the dev stack. |
| PL-TST-006 | **Gate spike.** The M2 spike measures replay latency and RSS on a 10 GB stream with 5k subjects under `mem_limit 900m`. |
| PL-TST-007 | **Restore drill.** M11 restore drill is a release gate (PL-BAK-007). |

### PL-DOC — Documentation

| ID | Requirement |
|---|---|
| PL-DOC-001 | **Operator docs.** `docs/operator/` covers configuration, deployment, keys, TLS, bans, backups, restore, upgrades, SSO and troubleshooting. |
| PL-DOC-002 | **API docs.** `docs/API.md` describes the public and admin APIs and links the generated OpenAPI. |
| PL-DOC-003 | **Envelope docs.** `docs/ENVELOPE.md` (PL-ENV-005). |
| PL-DOC-004 | **Sizing.** `docs/SIZING.md` with spike results and guidance. |
| PL-DOC-005 | **Backups.** `docs/BACKUPS.md` with the NATSARC1 format and the restore procedure. |

## 7. Definition of done

A requirement is **done** only when all of the following hold:

1. **Implemented**: the code is in a committed milestone on `main`.
2. **Tested**: the test layers listed for it in `docs/TEST_STRATEGY.md` exist and pass locally and in CI.
3. **Documented**: operator, API or SDK docs are updated where the behavior is user-visible.
4. **Tracked**: its box is checked in `docs/PLAN.md` §6 with a pointer to the evidence.

Status vocabulary, which must not be conflated:

- **Implemented**: code exists.
- **Tested**: automated tests in this repo pass.
- **Externally verified**: verified outside the test suite: on the real VPS, against real Cloudflare, Let's Encrypt,
  GitHub/Stripe deliveries or S3, or by an outside reviewer.

A milestone is done when all requirements it owns are done, `make check` is green, PLAN.md and DECISIONS.md are updated,
and it is committed locally.

## 8. Release gates (v1.0)

- RG-1: `make check` green, CI green on the release commit.
- RG-2: no fixable HIGH/CRITICAL vulnerabilities in the image (Trivy), govulncheck clean.
- RG-3: M2 spike results recorded and within the memory budget (PL-OPS-009).
- RG-4: M11 restore drill passes (PL-BAK-007).
- RG-5: externally verified on the test VM: firewall, bans, NATS TLS, AOP (M14).
- RG-6: no `auth.mode=none` exposure possible in the prod profile (PL-ADM-003 test).
