# Pulse — Implementation Plan

> **Status:** active execution plan. Created at M0 on 2026-10-23.
> The product contract is `docs/GOAL.md`; when the two disagree, GOAL.md wins. Fix this plan and record the change in DECISIONS.md.
> Decisions: `docs/DECISIONS.md` · Threats: `docs/THREAT_MODEL.md` · Tests: `docs/TEST_STRATEGY.md`.
> Progress lives in §5.2 (milestone status) and §6 (acceptance checklist). Keep both current.

Contents: 1 Summary · 2 Architecture · 3 Data model · 4 Key flows · 5 Milestones · 6 Acceptance checklist · 7 Risks · 8 Commit workflow

---

## 1. Summary and assumptions

Pulse is a NATS 2.15 JetStream server plus `pulsed`, a single Go binary that bootstraps NATS (operator mode), serves the
public HTTP edge and the loopback admin dashboard, computes rollups, enforces bans, runs webhooks, backups and alerts. Events
live in JetStream. pulsed's own state lives in SQLite (ADR-0024).

| # | Assumption | Consequence if false |
|---|---|---|
| A-1 | One VPS: 2 vCPU, 2 GB RAM, 50–100 GB disk, Debian 13. | Re-measure sizing (SIZING.md). |
| A-2 | Event volume ≤ ~1M events/day, ≤ 50k distinct subjects. | Rollup dimensions and subject index caps need revisiting. |
| A-3 | Cloudflare free plan in front of `events.wan.lt`. | More WAF and rate-limit rules become possible (R-03). |
| A-4 | Go 1.27.3 (ADR-0019), Node 24.22, pnpm 10. | Pin bumps go through CI. |
| A-5 | The operator reaches the admin UI with `ssh -L 8080:127.0.0.1:8080`. | OIDC (R-02) is required before exposure. |

## 2. Architecture

### 2.1 Components and processes (compose project `/srv/pulse`)

| service | command | network / ports | mem_limit |
|---|---|---|---|
| `init` | `pulsed init` (one-shot) | none | 128m |
| `nats` | `nats-server -c /data/nats/nats.conf` | bridge `pulse`; `4222` public; 8222 monitor and 8080 websocket internal | 900m (`GOMEMLIMIT` 600MiB) |
| `pulsed` | `pulsed serve` | `443→8443` public, `127.0.0.1:8080` admin, `127.0.0.1:9464` metrics/health/internal | 448m (300MiB) |
| `banagent` | `pulsed banagent` | host network, `NET_ADMIN`, polls `127.0.0.1:9464/internal/v1/bans` | 48m |
| `alloy` | (deployment repo) | host network, `127.0.0.1:12345` | 192m |

Start order: `init` (completed) → `nats` (healthy) → `pulsed` (healthy) → `banagent`.

### 2.2 Volumes and files

```
/data/pulsed            (pulsed volume, 0700, uid 65532)
  keys.enc              sealed key set (ADR-0017)
  pulse.db[-wal,-shm]   SQLite state (ADR-0024)
  pulsed.lock           flock
  banagent/token        bearer token for the loopback ban endpoint (0600); banagent mounts only this subpath
  tls/ca.crt            internal CA certificate (public)
  (internal user JWTs are minted in memory at start from keys.enc; no internal creds files on disk)
  acme/                 lego account + issued certs
/data/nats              (shared with nats, read-only for nats except jwt/ and js/)
  nats.conf  operator.jwt  tls/server.crt  tls/server.key  jwt/ (resolver dir)  js/ (JetStream store)
/run/secrets/pulse_master_key          compose secret, never on a volume
/run/secrets/cf_origin.{crt,key}, aop_ca.crt, cf_dns_token, s3_*, telegram_*, discord_*  (optional per feature)
```

### 2.3 Go packages

| Package | Responsibility |
|---|---|
| `internal/app` | Wiring for `serve`, version vars, lifecycle (start order, graceful shutdown). |
| `internal/config` | `PULSE_*` env + `*_FILE` secrets, profiles, validation (prod refusals). |
| `internal/logging` | slog JSON setup, redaction helpers. |
| `internal/secretbox` | Sealed key store (XChaCha20-Poly1305 + HKDF), atomic writes. |
| `internal/natsauth` | nkeys/JWT minting, grants → permissions, revocation, `.creds` rendering. |
| `internal/natsconf` | `nats.conf` rendering. |
| `internal/bootstrap` | `pulsed init`: keys, operator JWT, conf, bootstrap cert, internal creds, banagent token. |
| `internal/natsconn` | SYS and APP connections with TLS + creds, reconnect, status. |
| `internal/sysapi` | `$SYS.REQ.*` (VARZ, JSZ, CONNZ, CLAIMS.UPDATE/LOOKUP, RELOAD, KICK), advisories. |
| `internal/store` | SQLite: open, pragmas, flock, migrations, typed repositories, pruning. |
| `internal/streams` | EVENTS ensure, user stream and consumer management. |
| `internal/query`, `internal/tail` | Explorer engine and live tail sessions. |
| `internal/rollup` | Headers-only counting, flush to SQLite, subject index, forecast. |
| `internal/ingest`, `internal/hooks`, `internal/outbound`, `internal/netguard` | Token API, inbound hooks, outbound dispatcher, SSRF-safe dialer. |
| `internal/ban`, `internal/banagent` | Detector, store, CF list sync; nft reconciler. |
| `internal/certs`, `internal/edge` | Origin cert reload, AOP, ACME; listeners, real IP, middleware, ws proxy. |
| `internal/auth` | Admin `Authenticator` seam (`none`, `oidc` stub). |
| `internal/api` | Huma APIs (public + admin), SSE helpers. |
| `internal/audit`, `internal/alerts`, `internal/notify` | Audit log, alert rules, Telegram/Discord. |
| `internal/backup`, `internal/metrics`, `internal/ui` | Backups/restore; Prometheus; embedded UI. |
| `internal/testutil/natstest` | Embedded nats-server booted from the generated `nats.conf`. |
| `pkg/pulse`, `pkg/pulse/envelope` | Go SDK and the shared envelope. |

### 2.4 Listeners

| Listener | Default | Routes | Auth |
|---|---|---|---|
| public | `:8443` (TLS + AOP in prod; plain allowed in dev) | `/api/v1/*`, `/hooks/*`, `/ws`, `/healthz` | HTTP tokens, hook verifiers, NATS JWT over ws |
| admin | `0.0.0.0:8080` in-container, published `127.0.0.1:8080` | UI, `/api/admin/v1/*` | `Authenticator` (`none`) |
| metrics | `0.0.0.0:9464` in-container, published `127.0.0.1:9464` | `/metrics`, `/healthz`, `/readyz`, `/internal/v1/bans` | none / bearer for `/internal` |

### 2.5 Trust boundaries

See GOAL.md §4.3 and THREAT_MODEL.md.

## 3. Data model

### 3.1 NATS

| Object | Account | Notes |
|---|---|---|
| `EVENTS` stream | APP | `evt.>`, ADR-0003. |
| user streams | APP | created in the dashboard, no overlap with `evt.>`/`_sys.>`. |
| `blobs` Object Store | APP | large payloads (PL-BUS-004). |
| `_sys.alert.<rule>` | APP | core publish of alert events (not stored in a stream by default). |
| internal users | SYS: `pulsed-sys`; APP: `pulsed-app` | creds minted by init. |

### 3.2 SQLite schema (migration 0001+; see `internal/store/migrations`)

`meta`, `clients`, `client_grants`, `http_tokens`, `hooks_in`, `hooks_out`, `deliveries`, `bans`, `ban_offences`,
`ban_allowlist`, `audit`, `alert_rules`, `alert_events`, `notifiers`, `settings`, `rollup_1m`, `rollup_1h`, `rollup_1d`,
`rollup_checkpoints`, `subjects`, `backups`. Grants are stored as JSON arrays on their owner (clients, tokens) for simplicity.

## 4. Key flows

### 4.1 Bootstrap (`pulsed init`, idempotent)
1. Load master key. 2. Load or create `keys.enc` (operator, operator signing key, SYS/APP + signing keys, internal user keys,
internal CA, ACME account key). 3. Write `operator.jwt`, render `nats.conf` (SYS preloaded). 4. Issue the bootstrap leaf for 4222
if missing or expiring within 30 days (or keep the ACME cert). 5. Mint internal creds and `banagent.token` if missing.

### 4.2 `pulsed serve` start
Config → flock → SQLite (quick_check, migrate) → keys → connect SYS → push APP account JWT if missing/stale → connect APP →
ensure EVENTS and `blobs` → start rollups, outbound, bans, ACME, backups, alerts → listeners → ready.

### 4.3 Client lifecycle (ADR-0025)
Create: generate a per-client scoped signing key in APP whose scope template carries the client's permissions, generate the user
key, sign a permission-less user JWT with the scoped key, discard the scoped key's seed, push the APP account JWT, return `.creds`
once. Edit grants: rewrite the scope template in the APP JWT and push (no new creds). Rotate: new scoped key + user key, remove the
old scoped key. Revoke: remove the scoped key, add the user key to `revocations`, push, KICK connections.

### 4.4 Rollups
Ordered `HeadersOnly` consumer from checkpoint+1 → in-memory minute buckets → flush every 10 s in one transaction → RAM cache.

## 5. Milestones

### 5.1 Work packages

| M | Title | Scope | Done when |
|---|---|---|---|
| M0 | Skeleton | git init; GOAL/PLAN/DECISIONS/THREAT_MODEL/TEST_STRATEGY/AGENTS; go.mod, Makefile, Dockerfile, `internal/ui` embed, `pulsed version|healthcheck`; Vite + Mantine shell with theme; CI and release workflows; deployment repo skeleton docs. | `make check` green; image builds. |
| M1 | Identity, NATS bootstrap, SQLite | secretbox, natsauth, natsconf, bootstrap, natsconn, sysapi, store (SQLite), config, `compose.dev.yaml` (init → nats → pulsed). | Embedded-server tests: creds connect, AUTH.ERR host, revoke → disconnect, accounts persist over restart, RELOAD with new cert. |
| M2 | Envelope, EVENTS, Go SDK, CLI | envelope, EVENTS ensure, grants → permissions tests, SDK Publish/Subscribe(Since/Durable)/Handle[T], CLI context/pub/sub. Spike: 10 GB, 5k subjects, mem 900m → SIZING.md. | Tests green; spike recorded. |
| M3 | API and query engine | Huma groups, listeners, auth seam, `pulsed openapi` → TS types, explorer and SSE tail endpoints, table-driven query tests. | Tests green; spec generated. |
| M4 | UI core | Shell (sidebar, Cmd-K, theme), Explorer, Live tail, Streams & consumers; vitest + Playwright vs `make dev-up`. | Tests green. |
| M5 | Rollups | Rollup engine, Overview (KPIs, throughput 1h/24h/30d/1y, top subjects, forecast), Subject tree. | Tests green. |
| M6 | Clients and ingress | Clients & credentials UI, HTTP tokens, publish API, request bridge, audit log. | Tests green. |
| M7 | Webhooks | Inbound verifiers (GitHub/Stripe vectors), outbound dispatcher, delivery log, retry. | Tests green. |
| M8 | Security and edge TLS | Ban detector + enforcement, prod TLS with AOP (local CA + `curl --cert`), CF list sync (mocked), banagent, Security page. | Tests green. |
| M9 | ACME | lego + pebble profile, reload + verify, expiry in System. | Tests green. |
| M10 | Observability | `/metrics`, System page, alert rules, Telegram/Discord, Alloy in dev. | Tests green. |
| M11 | Backups | SQLite snapshot, segments + snapshots, SeaweedFS S3, GFS, restore drill (gate). | Drill passes or fallback ADR. |
| M12 | Remaining UI | KV/Object browser, Backups, Alerts, Settings, audit filters, Playwright smoke of every page. | Tests green. |
| M13 | Python and TS SDKs | Conformance vectors, interop e2e (Go → Python, TS node, browser via /ws). | Tests green. |
| M14 | Ansible (events-wan-lt) | base, firewall, fail2ban, docker, pulse, alloy roles; vm-verify.sh. | Needs test VM (externally verified). |
| M15 | Terraform + deploy (events-wan-lt) | Module, `terraform test` with mocks, examples, `make deploy`, GO-LIVE.md. | validate + test green. |

### 5.2 Status table

Status values: `planned`, `in-progress`, `implemented`, `tested`, `externally-verified`, `blocked`.

| M | Title | Status | Commit |
|---|---|---|---|
| M0 | Skeleton | tested | 0e5611e |
| M1 | Identity, NATS bootstrap, SQLite | tested | 02717c1 |
| M2 | Envelope, EVENTS, Go SDK, CLI | tested (spike pending user run) | 1e417c0 |
| M3 | API and query engine | tested | 9e15025 |
| M4 | UI core | tested | 4d21daf |
| M5 | Rollups | tested | 07f01fd |
| M6 | Clients and ingress | tested | 2378428 |
| M7 | Webhooks | tested | 26c5b2f |
| M8 | Security and edge TLS | tested (VM/Cloudflare pending) | bb92060 |
| M9 | ACME | tested against pebble (Let's Encrypt/Cloudflare pending) | (this commit) |
| M10 | Observability | planned | |
| M11 | Backups | planned | |
| M12 | Remaining UI | planned | |
| M13 | Python and TS SDKs | planned | |
| M14 | Ansible (events-wan-lt) | planned | n/a (no commits in that repo) |
| M15 | Terraform + deploy (events-wan-lt) | planned | n/a |

### 5.3 Progress log

| Date | M | Note |
|---|---|---|
| 2026-10-23 | M0 | Planning docs written. ADR-0024 (SQLite for pulsed state) recorded per user decision. ADR-0025 (scoped signing key per client). |
| 2026-10-23 | M0 | Skeleton done: Go module, `pulsed version|healthcheck`, UI shell, Dockerfile, CI/release. Go bumped to 1.27.3 for GO-2026-6809 (ADR-0019). `make check` green. |
| 2026-10-23 | M1 | secretbox, natsauth (grants→permissions, scoped signers), natsconf, bootstrap, natsconn, sysapi, store (SQLite), clients service, config, `pulsed init|serve|keys export`, dev compose (init → nats → pulsed). Found and fixed: empty NATS allow list = allow all (sentinel); scoped users need zero permission sections; upstream race in embedded reload (RELOAD tests use a subprocess). |
| 2026-10-23 | M2 | Envelope + conformance vectors, EVENTS/blobs ensure, Go SDK (publish, ordered/durable subscribe, Handle[T], blobs), CLI context/pub/sub. Spike tooling written but not run: the user decides when (heavy). |
| 2026-10-23 | M3 | Huma admin/public APIs, auth seam, Host allowlist (DNS rebinding), CSRF header, explorer engine (direct get batch, ordered consumer, backward windows, cursors, budgets), SSE tail, `pulsed openapi` + TS types (separate TS5 apigen package), UI served from the binary. |
| 2026-10-23 | M4 | Shell with NATS status indicator, Explorer (URL filters, virtualized table, cursor paging, budget notice, event drawer with payload views and replay command), Live tail (SSE, buffered rendering, sampling notices), Streams list/detail/create/edit/delete, consumers. vitest + Playwright (6 smoke tests vs dev stack). Seeded-data e2e waits for M6 (no client creds in the dev stack yet). |
| 2026-10-23 | M5 | Rollup engine (headers-only ordered consumers, exactly-once with checkpoint in the same SQLite tx, per-resolution dimensions, minute cache, key sampling, pruning), overview/throughput/subject tree APIs, admin publish endpoint, Overview (KPIs, ECharts throughput, rank lists, forecast) and Subjects pages; data-seeded e2e (9 tests). |
| 2026-10-24 | M6 | HTTP tokens (hashed, CIDR-limited), public publish/batch/request/replay/tail with grant checks and an auth-failure hook for M8, clients/tokens/audit admin APIs, Clients (grants editor, show-once creds + snippets, rotate, revoke, live connections), HTTP tokens and Audit pages; e2e publishes through the public API with an issued token. Python/TS snippets describe the M13 SDK API and must match it. |
| 2026-10-24 | M7 | Inbound hooks (GitHub, Stripe, HMAC, shared secret; sealed secrets; 401 feeds the ban hook), outbound dispatcher (ordered, backoff 1 s–1 h, 12 attempts, dead letters, HMAC signing, SSRF guard checked at dial time, delivery log, pause/resume, retry, test), admin API and Webhooks page; e2e covers inbound delivery and outbound test/pause/delete. |
| 2026-10-24 | M8 | Ban detector (NATS AUTH.ERR, HTTP 401, webhook signatures; window, ×4 escalation, never-ban), SQLite bans/allowlist, HTTP 403 middleware, banagent (long-poll feed, nftables interval sets with timeouts; real nft tested in a privileged container), Cloudflare IP-list reconciler (fake API), origin cert hot reload, AOP mTLS (handshake tests), Cloudflare-aware real IP, /ws proxy (NATS over websocket through the proxy), Security page. |
| 2026-10-24 | M9 | ACME manager on lego (account by key from `keys.enc`, DNS-01 via Cloudflare or challtestsrv), ACME pair listed before the internal leaf in `nats.conf` and selected by SNI (ADR-0032 amends ADR-0023), atomic write → re-render → RELOAD → served-serial check over a raw INFO/TLS handshake, renewal at min(30 d, lifetime/3) (found with pebble's 6-day certificates), `GET /system/certificates`, System page (server, NATS, JetStream, certificates, ACME status). `make test-acme` runs pebble + challtestsrv in Docker against a real nats-server. Canceled requests now return 499 instead of logging a 500. `make secrets` was scanning 0 commits locally (global `color.ui=always` breaks gitleaks' log parser); it now runs with `GIT_CONFIG_GLOBAL=/dev/null` and scans all 9 commits clean. |

## 6. Acceptance checklist (mapped to requirement IDs)

A box is checked only when the requirement meets the definition of done (GOAL.md §7). Append the evidence after the line,
e.g. `— ✅ tested @ abc1234 (internal/natsauth/perm_test.go)`. Evidence codes: U unit · I integration (embedded NATS) ·
E browser E2E · C CI/static check · S spike/perf · R restore drill · X external verification (VM, Cloudflare, real providers) · D docs.


#### PL-BUS — Streams and subjects

- [x] `PL-BUS-001` EVENTS stream — M2 — ✅ tested: internal/streams TestEnsureEvents (config, drift correction, grow, shrink refused/confirmed)
- [x] `PL-BUS-002` Subject grammar — M2 — ✅ tested: pkg/pulse/envelope conformance subjects; SDK TestDeduplicationAndValidation
- [x] `PL-BUS-003` Raw publishes accepted — M2 — ✅ tested: pkg/pulse TestRawPublishIsUntyped; envelope decode vector
- [x] `PL-BUS-004` Payload size — M2 — ✅ tested: pkg/pulse TestBlobs (3.2 MB via Object Store, checksum, no access without grant)
- [x] `PL-BUS-005` Extra streams — M4 — ✅ tested: internal/api TestStreamsCRUD; e2e streams create/delete and overlap rejection
- [x] `PL-BUS-006` Consumers — M4 — ✅ tested: internal/api TestStreamsCRUD (list/delete consumer); UI consumers table
- [x] `PL-BUS-007` Replay by time — M2 — ✅ tested: pkg/pulse TestPublishReplaySince; cmd/pulse TestCLIPubSubFlow
- [x] `PL-BUS-008` Internal state isolation — M1 — ✅ tested: state in SQLite (internal/store), nothing internal stored in NATS
- [ ] `PL-BUS-009` Subject cardinality guidance — M2 — ◐ provisional guideline in docs/SIZING.md; to be confirmed by the spike

#### PL-ENV — Envelope

- [x] `PL-ENV-001` Headers — M2 — ✅ tested: pkg/pulse/envelope conformance encode/decode
- [ ] `PL-ENV-002` Shared encoder — M13
- [x] `PL-ENV-003` Idempotency — M2 — ✅ tested: pkg/pulse TestDeduplicationAndValidation
- [x] `PL-ENV-004` Blob references — M2 — ✅ tested: envelope blob vector; pkg/pulse TestBlobs
- [x] `PL-ENV-005` Documentation — M2 — ✅ documented: docs/ENVELOPE.md

#### PL-KEY — Key management and bootstrap

- [x] `PL-KEY-001` Sealed key store — M1 — ✅ tested: internal/secretbox (round trip, tamper, wrong key), internal/bootstrap keyset
- [x] `PL-KEY-002` Master key handling — M1 — ✅ tested: internal/config (prod file-only, missing key), bootstrap TestInitWrongMasterKeyFailsClosed
- [x] `PL-KEY-003` Idempotent init — M1 — ✅ tested: internal/bootstrap TestInitIdempotent; dev stack `docker compose run init` → "nothing to do"
- [x] `PL-KEY-004` Bootstrap artifacts — M1 — ✅ tested: internal/bootstrap TestInitIdempotent (conf, modes, banagent token), TestBootstrapLeafRenewal
- [x] `PL-KEY-005` Account sync — M1 — ✅ tested: internal/app TestStartReadyShutdown, internal/clients TestAccountsPersistOverRestart
- [x] `PL-KEY-006` Signing keys — M1 — ✅ tested: operator JWT has strict signing key usage; every integration test connects through signing-key-signed accounts/users
- [x] `PL-KEY-007` Key export — M1 — ✅ tested: cmd/pulsed TestInitAndKeysExport (confirmation flag, audit record)

#### PL-AUTH — Tool client identity

- [x] `PL-AUTH-001` Operator mode — M1 — ✅ tested: internal/clients integration tests (embedded server from generated nats.conf)
- [x] `PL-AUTH-002` Client records — M6 — ✅ tested: internal/clients; internal/api TestClientsAndAuditAPI
- [x] `PL-AUTH-003` Least privilege — M2 — ✅ tested: natsauth TestPermissionsNoGrants (sentinel); clients TestCredsConnectAndLeastPrivilege
- [x] `PL-AUTH-004` Credentials once — M6 — ✅ tested: creds returned once by create/rotate (internal/api); seed never stored (clients table has keys only); e2e show-once modal
- [x] `PL-AUTH-005` Grants to permissions — M2 — ✅ tested: natsauth TestPermissionsByAction; pkg/pulse durable/ordered/blobs with real permissions
- [x] `PL-AUTH-006` Explicit denies — M2 — ✅ tested: natsauth TestPermissionsNoGrants; clients least-privilege violations ($JS stream delete, $KV)
- [x] `PL-AUTH-007` Revocation — M6 — ✅ tested: internal/clients TestRevokeDisconnects (< 5 s); API + e2e revoke
- [x] `PL-AUTH-008` Rotation — M6 — ✅ tested: internal/clients TestRotate; API + e2e rotate
- [x] `PL-AUTH-009` Grant edits — M6 — ✅ tested: internal/clients TestGrantEditReconnectsConnectedClient; API version conflict 409; e2e grants edit
- [x] `PL-AUTH-010` Connection view — M6 — ✅ implemented + tested: /clients/{id}/connections (CONNZ by user key), list connection counts; UI table (populated path covered by internal/clients Connections test)
- [x] `PL-AUTH-011` Snippets — M6 — ✅ tested: web/src/lib/snippets.test.ts; e2e creds modal shows snippets
- [ ] `PL-AUTH-012` Expiry warnings — M6 — ◐ partial: expires_soon flag in API and UI warnings; alert event arrives with M10

#### PL-ADM — Dashboard access

- [x] `PL-ADM-001` Loopback admin listener — M3 — ✅ tested: internal/app (admin listener), dev stack on loopback; internal/api TestUIAndPublic (admin paths 404 on public)
- [x] `PL-ADM-002` Authenticator seam — M3 — ✅ tested: internal/auth seam; oidc refused (internal/config TestProdRefusals)
- [x] `PL-ADM-003` Refuse unsafe exposure — M3 — ✅ tested: internal/config TestProdRefusals (admin public / non-loopback with auth none)
- [x] `PL-ADM-004` Browser hardening — M3 — ✅ tested: internal/api TestAdminGuards (CSP and headers, CSRF header, Host allowlist)
- [x] `PL-ADM-005` Future OIDC contract — M3 — ✅ documented: docs/operator/sso.md

#### PL-EDGE — Public listener and TLS

- [x] `PL-EDGE-001` Public routes only — M3 — ✅ tested: internal/api TestUIAndPublic (route allowlist)
- [x] `PL-EDGE-002` Origin certificate — M8 — ✅ tested: internal/edge TestCertReloader (mtime reload, broken pair keeps the old one); app TLS test
- [x] `PL-EDGE-003` Authenticated Origin Pulls — M8 — ✅ tested: internal/edge TestAuthenticatedOriginPulls (no cert / foreign CA rejected), internal/app TestPublicTLSWithAOPAndAuthErrors; real Cloudflare pending (X)
- [x] `PL-EDGE-004` Real client IP — M8 — ✅ tested: internal/edge TestRealIP (CF peer + verified mTLS only)
- [x] `PL-EDGE-005` WebSocket proxy — M8 — ✅ tested: internal/edge TestWSProxyRewritesForwardedFor; internal/api TestWebSocketProxy (NATS client over /ws)
- [x] `PL-EDGE-006` Raw NATS TLS — M9 — ◐ tested against pebble: internal/acme TestPebbleObtainInstallReload (`make test-acme`: DNS-01 obtain, nats.conf re-render, RELOAD of a real nats-server, served serial for the raw host, internal leaf still served for SNI `nats`, no re-issue when fresh, renewal), TestDue (30 d / lifetime-third window), internal/bootstrap TestACMEPairListedFirst, internal/config TestACME (prod refuses challtestsrv), internal/api TestCertificates; Let's Encrypt + Cloudflare DNS pending (X, M15)
- [x] `PL-EDGE-007` Bootstrap TLS — M1 — ✅ tested: internal/bootstrap TestBootstrapLeafRenewal; internal/clients TestReloadServesNewCert
- [x] `PL-EDGE-008` Cloudflare ranges — M8 — ✅ tested: embedded ranges, FetchCloudflare validation (internal/edge); daily refresh loop wired
- [x] `PL-EDGE-009` Timeouts and limits — M3 — ✅ tested: timeouts, body limits, per-IP token bucket on the real client IP (internal/api)
- [x] `PL-EDGE-010` Dev mode — M3 — ✅ tested: prod refuses plain HTTP and missing AOP (internal/config); dev plain HTTP works

#### PL-BAN — Automatic bans

- [x] `PL-BAN-001` Detection sources — M8 — ✅ tested: AUTH.ERR → detector (internal/app), HTTP 401 → ban (internal/api), webhook failures hook (internal/hooks)
- [x] `PL-BAN-002` Sliding windows — M8 — ✅ tested: internal/ban TestThresholdWindowAndGrouping
- [x] `PL-BAN-003` Escalation — M8 — ✅ tested: internal/ban TestEscalationAndCap (×4, 30-day cap, reset after memory)
- [x] `PL-BAN-004` Never-ban list — M8 — ✅ tested: internal/ban TestNeverBan (loopback, private, Docker, ULA, Cloudflare, config); allowlist in TestManualAllowlistAndPersistence
- [x] `PL-BAN-005` Storage — M8 — ✅ tested: SQLite persistence across restart, manual ban/lift audited (internal/ban, internal/api)
- [x] `PL-BAN-006` HTTP enforcement — M8 — ✅ tested: internal/ban TestMiddlewareAndFeed; internal/api TestSecurityAPIAndEnforcement
- [ ] `PL-BAN-007` nftables enforcement — M8 — ◐ tested: banagent unit tests + real nftables in a privileged Linux container (make test-nft); not yet externally verified on the VM (M14)
- [x] `PL-BAN-008` Cloudflare list (optional) — M8 — ✅ tested: internal/ban TestCloudflareReconcile (fake API: add/remove managed items, unmanaged untouched, idempotent); real Cloudflare pending (X)
- [x] `PL-BAN-009` Security page — M8 — ✅ tested: e2e security.spec.ts (failures, ban, lift, never-ban error, allowlist)

#### PL-ING — HTTP ingress

- [x] `PL-ING-001` HTTP tokens — M6 — ✅ tested: internal/tokens TestLifecycle/TestExpiryAndValidation (hash, prefix, CIDR, expiry, revoke)
- [x] `PL-ING-002` Publish — M6 — ✅ tested: internal/api TestIngest (envelope headers, idempotency); e2e publish with issued token
- [x] `PL-ING-003` Batch publish — M6 — ✅ tested: internal/api TestIngest batch (payload kinds, all-or-nothing authorization)
- [x] `PL-ING-004` Request bridge — M6 — ✅ tested: internal/api TestIngest request bridge (reply, 503, 403)
- [x] `PL-ING-005` HTTP replay — M6 — ✅ tested: internal/api TestIngest replay (paged, grant subset)
- [x] `PL-ING-006` SSE tail — M6 — ✅ tested: internal/api TestPublicTailAuth (401/403 before stream, event delivery)
- [x] `PL-ING-007` Authorization — M6 — ✅ tested: internal/api TestIngest/TestTokenCIDR (401 + WWW-Authenticate + auth-failure hook, 403)
- [x] `PL-ING-008` Audit — M6 — ✅ tested: token create/revoke audited (internal/tokens), audit API

#### PL-HOOK — Webhooks

- [x] `PL-HOOK-001` Named inbound hooks — M7 — ✅ tested: internal/hooks TestGitHubDeliveryPublishesEvent, TestStripeAndSecretHooks; internal/api TestWebhooksAPI; e2e inbound
- [x] `PL-HOOK-002` Verifiers — M7 — ✅ tested: internal/hooks verify tests (all four verifiers)
- [ ] `PL-HOOK-003` Verifier tests — M7 — ◐ tested: GitHub doc vector (internal/hooks TestGitHubDocVector); Stripe uses documented-scheme vectors (no published fixed vector, ADR-0030); live deliveries not externally verified
- [x] `PL-HOOK-004` Failure handling — M7 — ✅ tested: 401 without detail, failure hook called, Stripe/HMAC tolerance enforced
- [x] `PL-HOOK-005` Outbound hooks — M7 — ✅ tested: internal/outbound TestDeliveryOrderSignatureAndRetry (in order, one in flight)
- [x] `PL-HOOK-006` Retries — M7 — ✅ tested: retry then success; TestDeadLetterThenNext (dead letter, next event proceeds); default schedule asserted
- [x] `PL-HOOK-007` Signing — M7 — ✅ tested: signature recomputed by the receiver in TestDeliveryOrderSignatureAndRetry
- [x] `PL-HOOK-008` SSRF guard — M7 — ✅ tested: internal/netguard (blocklist, dial-time check, allowlist); internal/outbound TestSSRFGuard
- [x] `PL-HOOK-009` Delivery log — M7 — ✅ tested: delivery log entries, excerpts, manual retry (outbound + API); 30-day prune implemented
- [x] `PL-HOOK-010` Pause/resume — M7 — ✅ tested: internal/outbound TestPauseResumeKeepsPosition; e2e pause

#### PL-API — API

- [x] `PL-API-001` Huma v2 on net/http — M3 — ✅ tested: internal/api (Huma v2 on net/http)
- [x] `PL-API-002` Spec export — M3 — ✅ tested: `pulsed openapi`, `make api`, CI drift check (api/, web/src/api)
- [x] `PL-API-003` Errors — M3 — ✅ tested: problem details in internal/api tests; internalErr hides internals
- [x] `PL-API-004` Live data — M3 — ✅ tested: internal/api TestTailSSE (events, resume, keepalive design)
- [x] `PL-API-005` Versioning — M3 — ✅ implemented: /api/v1 and /api/admin/v1 prefixes

#### PL-EXP — Explorer and tail

- [x] `PL-EXP-001` Forward reads — M3 — ✅ tested: internal/query TestQueries (forward single filter via GetBatch)
- [x] `PL-EXP-002` Multi-filter reads — M3 — ✅ tested: internal/query TestQueries (two-subject cases use an ordered consumer)
- [x] `PL-EXP-003` Newest first — M3 — ✅ tested: internal/query TestQueries backward + TestBackwardEndsAt
- [x] `PL-EXP-004` Opaque cursor — M3 — ✅ tested: internal/query TestCursorTampering; internal/api mismatched cursor → 400
- [x] `PL-EXP-005` Server-side filters — M3 — ✅ tested: internal/query TestQueries (type, untyped, header, contains, json path)
- [x] `PL-EXP-006` Budgets — M3 — ✅ tested: internal/query TestBudgetPartialAndResume, TestConcurrencyLimit
- [x] `PL-EXP-007` Live tail — M3 — ✅ tested: internal/api TestTailSSE (session cap, resume); rate-limit notices implemented, not load-tested
- [x] `PL-EXP-008` Message view — M4 — ✅ tested: PayloadView vitest (JSON/text/hex); EventDrawer with copyable `pulse sub … --seq`; e2e explorer render

#### PL-ROLL — Rollups

- [x] `PL-ROLL-001` Headers-only counting — M5 — ✅ tested: internal/rollup TestCountsExactlyOnceAcrossRestart (headers-only, checkpoint resume)
- [x] `PL-ROLL-002` Dimensions — M5 — ✅ tested: internal/rollup (totals, p1–p4 per resolution, types incl. untyped)
- [x] `PL-ROLL-003` Storage — M5 — ✅ tested: internal/rollup restart + TestCrashLosesNothingAndCountsOnce; minute cache
- [x] `PL-ROLL-004` Subject index — M5 — ✅ tested: internal/rollup TestSubjectCapAndTree (cap + overflow), TestSampleKeysAndPrune (JSON keys)
- [x] `PL-ROLL-005` Forecast — M5 — ✅ tested: internal/rollup TestComputeForecast; Overview forecast card (horizon shown after ≥ 1 day)
- [x] `PL-ROLL-006` Overview — M5 — ✅ tested: internal/api TestRollupEndpoints; e2e overview with seeded data
- [x] `PL-ROLL-007` Subject tree — M5 — ✅ tested: internal/rollup TestSubjectCapAndTree; e2e subject tree expand + detail

#### PL-OBS — Observability

- [ ] `PL-OBS-001` Logs — M1 — ◐ implemented: internal/logging (slog JSON); redaction review pending
- [ ] `PL-OBS-002` Metrics — M10
- [x] `PL-OBS-003` Health — M1 — ✅ tested: internal/app TestStartReadyShutdown (/healthz, /readyz); dev stack container health
- [ ] `PL-OBS-004` Alert rules — M10
- [ ] `PL-OBS-005` Alert events — M10
- [ ] `PL-OBS-006` Notifiers — M10
- [ ] `PL-OBS-007` System page — M10
- [ ] `PL-OBS-008` Alloy — M10

#### PL-BAK — Backups

- [ ] `PL-BAK-001` Schedule — M11
- [ ] `PL-BAK-002` Encryption — M11
- [ ] `PL-BAK-003` SQLite and small streams — M11
- [ ] `PL-BAK-004` EVENTS segments — M11
- [ ] `PL-BAK-005` Retention — M11
- [ ] `PL-BAK-006` Restore — M11
- [ ] `PL-BAK-007` Timestamp preservation gate — M11
- [ ] `PL-BAK-008` Catalog — M11
- [ ] `PL-BAK-009` S3 compatibility — M11

#### PL-DB — pulsed state database

- [x] `PL-DB-001` Driver and durability — M1 — ✅ tested: internal/store TestPragmasOnEveryConnection
- [x] `PL-DB-002` Migrations — M1 — ✅ tested: internal/store TestMigrationsAndReopen, TestRefuseNewerSchema
- [x] `PL-DB-003` Single owner — M1 — ✅ tested: internal/store TestExclusiveLock; internal/app second Start refused
- [ ] `PL-DB-004` No network I/O in transactions — M1 — ◐ implemented: clients service prepares keys before Write and pushes after commit; no analyzer enforces it yet
- [ ] `PL-DB-005` Retention — M1 — ◐ partial: revocations, rollups (8 d / 400 d), audit (400 d), delivery log (30 d), expired bans and offences (30 d) pruned; alert history arrives with M10
- [ ] `PL-DB-006` Permissions and placement — M1 — ◐ partial: 0700/0600 modes tested (TestDatabaseFileMode); Linux network-FS refusal implemented, untested
- [x] `PL-DB-007` Integrity — M1 — ✅ tested: internal/store TestCorruptDatabaseRefused

#### PL-UI — Dashboard

- [ ] `PL-UI-001` Stack — M0 — ◐ implemented: Vite 8 + Mantine 8 + TanStack Router/Query/Table-ready/Virtual + embed; ECharts arrives with M5 charts
- [ ] `PL-UI-002` Look — M0 — ◐ implemented: theme (4px radius, compact inputs, Inter/JetBrains Mono, indigo, light/dark), checked by screenshots; no visual regression test
- [x] `PL-UI-003` Shell — M4 — ✅ tested: e2e shell (status indicator, theme toggle, Cmd-K navigation); collapsible sidebar
- [ ] `PL-UI-004` Pages — M12
- [x] `PL-UI-005` Typed API — M4 — ✅ tested: web/src/api/client.ts (openapi-fetch + generated types), typecheck in CI
- [ ] `PL-UI-006` Charts — M4 — ◐ implemented: ECharts (SVG) themed from Mantine tokens per color scheme, palette validated with the dataviz validator (light/dark); no visual regression test
- [x] `PL-UI-007` Large lists — M4 — ✅ implemented: VirtualTable (@tanstack/react-virtual) for explorer/tail; e2e renders it
- [x] `PL-UI-008` Secret display — M4 — ✅ tested: SecretModal (copy/download, dropped from state on close); e2e checks the secret disappears
- [ ] `PL-UI-009` Tests — M12
- [ ] `PL-UI-010` Missing UI — M0 — ◐ implemented: internal/api uiHandler serves a notice when the UI is not built; that path is untested (tests run with a built UI); SPA fallback tested

#### PL-SDK — SDKs and CLI

- [x] `PL-SDK-001` Go SDK — M2 — ✅ tested: pkg/pulse integration tests (publish, since, durable + redelivery, Handle[T], blobs); Request untested
- [ ] `PL-SDK-002` Python SDK — M13
- [ ] `PL-SDK-003` TS SDK — M13
- [ ] `PL-SDK-004` Conformance — M13
- [ ] `PL-SDK-005` Interop e2e — M13
- [ ] `PL-SDK-006` CLI — M2 — ◐ partial: `context`, `pub`, `sub --since/--seq/--new/--durable/--json` tested (cmd/pulse); `req`, `streams`, `tail`, `clients`, `backup` pending
- [x] `PL-SDK-007` Inbox prefix — M2 — ✅ tested: pkg/pulse (inbox prefix from creds tag; client permissions only allow _INBOX_<id>)

#### PL-OPS — Packaging and operations

- [x] `PL-OPS-001` Image — M0 — ✅ tested (local): `make image` builds distroless static-debian13, digests pinned, user 65532:65532; CI image job implemented, not yet run on GitHub
- [ ] `PL-OPS-002` Commands — M0 — ◐ partial: version, healthcheck, init, serve, keys export, openapi, banagent implemented; banagent only unit/nft-container tested
- [x] `PL-OPS-003` Configuration — M1 — ✅ tested: internal/config tests; docs/operator/configuration.md
- [ ] `PL-OPS-004` Profiles — M1 — ◐ partial: prod refusals for admin exposure, plain master key, missing key, plain-HTTP public listener and missing AOP CA tested (internal/config); dev master key detection not applicable (prod requires *_FILE)
- [ ] `PL-OPS-005` Dev stack — M1 — ◐ partial: deploy/compose.dev.yaml runs init → nats → pulsed (verified locally); SeaweedFS (M11) pending; pebble runs in `make test-acme`, not in the dev stack (ADR-0032)
- [ ] `PL-OPS-006` Reference prod compose — M1 — ◐ implemented: deploy/examples/compose.yaml; banagent service depends on M8
- [ ] `PL-OPS-007` CI — M0 — ◐ implemented: .github/workflows/ci.yaml (SHA-pinned, GOTOOLCHAIN=local); `make check` green locally; never run on GitHub (no remote)
- [ ] `PL-OPS-008` Release — M0 — ◐ implemented: .github/workflows/release.yaml; not externally verified (no remote, no tag)
- [ ] `PL-OPS-009` Memory budget — M2 — ◐ limits configured (deploy/examples, deploy/compose.dev.yaml); measurement pending the operator-triggered spike (docs/SIZING.md)
- [ ] `PL-OPS-010` Graceful shutdown — M1 — ◐ partial: listeners and NATS drain; rollup workers flush on shutdown before the DB closes (internal/app); outbound consumers arrive with M7
- [x] `PL-OPS-011` Versioning — M0 — ✅ tested: cmd/pulsed/main_test.go TestVersionJSON; docs/VERSIONING.md

#### PL-AUD — Audit

- [x] `PL-AUD-001` Audit stream — M6 — ✅ tested: audit rows for client, token, stream, consumer, event publish and key export actions
- [ ] `PL-AUD-002` Audit page — M12 — ◐ implemented: Audit page with action/actor/target/result filters and paging; time-range filter UI pending M12
- [x] `PL-AUD-003` No secrets — M6 — ✅ tested: TestClientsAndAuditAPI asserts no seed in audit output; details carry names/grants/keys only

#### PL-TST — Testing

- [x] `PL-TST-001` Embedded server tests — M1 — ✅ tested: internal/testutil/natstest
- [x] `PL-TST-002` Race — M1 — ✅ tested: `make test` (-race) green
- [x] `PL-TST-003` Bootstrap tests — M1 — ✅ tested: internal/clients (creds connect, AUTH.ERR host, revoke → disconnect < 5 s, restart persistence, RELOAD new cert)
- [x] `PL-TST-004` Query tests — M3 — ✅ tested: internal/query (72 table-driven subtests)
- [x] `PL-TST-005` E2E — M4 — ✅ tested: web/e2e/smoke.spec.ts (6 tests) vs `make dev-up`; .github/workflows/e2e.yaml (not yet run on GitHub)
- [ ] `PL-TST-006` Gate spike — M2 — ◐ tooling ready: cmd/pulse-spike, scripts/spike.sh, `make spike`; not run — the user triggers it
- [ ] `PL-TST-007` Restore drill — M11

#### PL-DOC — Documentation

- [ ] `PL-DOC-001` Operator docs — M12
- [x] `PL-DOC-002` API docs — M3 — ✅ documented: docs/API.md
- [x] `PL-DOC-003` Envelope docs — M2 — ✅ documented: docs/ENVELOPE.md
- [ ] `PL-DOC-004` Sizing — M2 — ◐ docs/SIZING.md procedure + budget; results pending the spike run
- [ ] `PL-DOC-005` Backups — M11

## 7. Risks

| # | Risk | Check | Fallback |
|---|---|---|---|
| K-1 | NATS memory with a large file store and many subjects on 2 GB. | M2 spike (PL-TST-006): tooling ready (`make spike`), run is operator-triggered. | Subject cardinality guideline; lower `max_bytes`; bigger VPS. |
| K-2 | Restored segments lose `Nats-Time-Stamp`. | M11 drill (PL-BAK-007). | Full snapshots only (ADR). |
| K-3 | Docker + nftables interplay, real client IP on IPv6. | M14 on the test VM. | nats with `network_mode: host`. |
| K-4 | CF free-plan limits (custom rules, one rate-limit rule with a 10 s period, one IP list). | M15 module design. | R-03. |
| K-5 | `cloudflare_api_token` needs a parent token with API Tokens: Edit. | M15 docs. | Create tokens manually. |
| K-6 | Vite 8 (rolldown) with the TanStack router plugin and the Mantine PostCSS preset. | M0 build. | Code-based routes. |
| K-7 | Huma SSE's per-message write deadline. | Cleared at M3: the deadline is set per write, keepalives every 20 s; tail SSE tested. | Plain `http.Flusher` handler outside Huma, documented in the spec. |
| K-8 | Installing SDKs from git (pnpm `github:…#path:/sdk/ts`, pip `#subdirectory=`). | M13. | Publish to a private registry. |
| K-9 | SQLite single writer under bursty admin + rollup + delivery-log writes. | M5/M7 load tests. | Batch delivery-log writes. |

## 8. Commit workflow (local only)

1. `git status` and inspect the diff. Include only the milestone's files; no secrets.
2. `git add <explicit paths>`.
3. Generate the message with `/cc` (Conventional Commits). `/cc` prints a message only.
4. `git commit` with that message. 5. Report the hash. **Never push**, never create remotes, tags on remotes or PRs.
