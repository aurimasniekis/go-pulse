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
| M1 | Identity, NATS bootstrap, SQLite | tested | (this commit) |
| M2 | Envelope, EVENTS, Go SDK, CLI | planned | |
| M3 | API and query engine | planned | |
| M4 | UI core | planned | |
| M5 | Rollups | planned | |
| M6 | Clients and ingress | planned | |
| M7 | Webhooks | planned | |
| M8 | Security and edge TLS | planned | |
| M9 | ACME | planned | |
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

## 6. Acceptance checklist (mapped to requirement IDs)

A box is checked only when the requirement meets the definition of done (GOAL.md §7). Append the evidence after the line,
e.g. `— ✅ tested @ abc1234 (internal/natsauth/perm_test.go)`. Evidence codes: U unit · I integration (embedded NATS) ·
E browser E2E · C CI/static check · S spike/perf · R restore drill · X external verification (VM, Cloudflare, real providers) · D docs.


#### PL-BUS — Streams and subjects

- [ ] `PL-BUS-001` EVENTS stream — M2
- [ ] `PL-BUS-002` Subject grammar — M2
- [ ] `PL-BUS-003` Raw publishes accepted — M2
- [ ] `PL-BUS-004` Payload size — M2
- [ ] `PL-BUS-005` Extra streams — M4
- [ ] `PL-BUS-006` Consumers — M4
- [ ] `PL-BUS-007` Replay by time — M2
- [x] `PL-BUS-008` Internal state isolation — M1 — ✅ tested: state in SQLite (internal/store), nothing internal stored in NATS
- [ ] `PL-BUS-009` Subject cardinality guidance — M2

#### PL-ENV — Envelope

- [ ] `PL-ENV-001` Headers — M2
- [ ] `PL-ENV-002` Shared encoder — M13
- [ ] `PL-ENV-003` Idempotency — M2
- [ ] `PL-ENV-004` Blob references — M2
- [ ] `PL-ENV-005` Documentation — M2

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
- [ ] `PL-AUTH-002` Client records — M6
- [ ] `PL-AUTH-003` Least privilege — M2
- [ ] `PL-AUTH-004` Credentials once — M6
- [ ] `PL-AUTH-005` Grants to permissions — M2
- [ ] `PL-AUTH-006` Explicit denies — M2
- [ ] `PL-AUTH-007` Revocation — M6
- [ ] `PL-AUTH-008` Rotation — M6
- [ ] `PL-AUTH-009` Grant edits — M6
- [ ] `PL-AUTH-010` Connection view — M6
- [ ] `PL-AUTH-011` Snippets — M6
- [ ] `PL-AUTH-012` Expiry warnings — M6

#### PL-ADM — Dashboard access

- [ ] `PL-ADM-001` Loopback admin listener — M3
- [ ] `PL-ADM-002` Authenticator seam — M3
- [ ] `PL-ADM-003` Refuse unsafe exposure — M3
- [ ] `PL-ADM-004` Browser hardening — M3
- [ ] `PL-ADM-005` Future OIDC contract — M3

#### PL-EDGE — Public listener and TLS

- [ ] `PL-EDGE-001` Public routes only — M3
- [ ] `PL-EDGE-002` Origin certificate — M8
- [ ] `PL-EDGE-003` Authenticated Origin Pulls — M8
- [ ] `PL-EDGE-004` Real client IP — M8
- [ ] `PL-EDGE-005` WebSocket proxy — M8
- [ ] `PL-EDGE-006` Raw NATS TLS — M9
- [x] `PL-EDGE-007` Bootstrap TLS — M1 — ✅ tested: internal/bootstrap TestBootstrapLeafRenewal; internal/clients TestReloadServesNewCert
- [ ] `PL-EDGE-008` Cloudflare ranges — M8
- [ ] `PL-EDGE-009` Timeouts and limits — M3
- [ ] `PL-EDGE-010` Dev mode — M3

#### PL-BAN — Automatic bans

- [ ] `PL-BAN-001` Detection sources — M8
- [ ] `PL-BAN-002` Sliding windows — M8
- [ ] `PL-BAN-003` Escalation — M8
- [ ] `PL-BAN-004` Never-ban list — M8
- [ ] `PL-BAN-005` Storage — M8
- [ ] `PL-BAN-006` HTTP enforcement — M8
- [ ] `PL-BAN-007` nftables enforcement — M8
- [ ] `PL-BAN-008` Cloudflare list (optional) — M8
- [ ] `PL-BAN-009` Security page — M8

#### PL-ING — HTTP ingress

- [ ] `PL-ING-001` HTTP tokens — M6
- [ ] `PL-ING-002` Publish — M6
- [ ] `PL-ING-003` Batch publish — M6
- [ ] `PL-ING-004` Request bridge — M6
- [ ] `PL-ING-005` HTTP replay — M6
- [ ] `PL-ING-006` SSE tail — M6
- [ ] `PL-ING-007` Authorization — M6
- [ ] `PL-ING-008` Audit — M6

#### PL-HOOK — Webhooks

- [ ] `PL-HOOK-001` Named inbound hooks — M7
- [ ] `PL-HOOK-002` Verifiers — M7
- [ ] `PL-HOOK-003` Verifier tests — M7
- [ ] `PL-HOOK-004` Failure handling — M7
- [ ] `PL-HOOK-005` Outbound hooks — M7
- [ ] `PL-HOOK-006` Retries — M7
- [ ] `PL-HOOK-007` Signing — M7
- [ ] `PL-HOOK-008` SSRF guard — M7
- [ ] `PL-HOOK-009` Delivery log — M7
- [ ] `PL-HOOK-010` Pause/resume — M7

#### PL-API — API

- [ ] `PL-API-001` Huma v2 on net/http — M3
- [ ] `PL-API-002` Spec export — M3
- [ ] `PL-API-003` Errors — M3
- [ ] `PL-API-004` Live data — M3
- [ ] `PL-API-005` Versioning — M3

#### PL-EXP — Explorer and tail

- [ ] `PL-EXP-001` Forward reads — M3
- [ ] `PL-EXP-002` Multi-filter reads — M3
- [ ] `PL-EXP-003` Newest first — M3
- [ ] `PL-EXP-004` Opaque cursor — M3
- [ ] `PL-EXP-005` Server-side filters — M3
- [ ] `PL-EXP-006` Budgets — M3
- [ ] `PL-EXP-007` Live tail — M3
- [ ] `PL-EXP-008` Message view — M4

#### PL-ROLL — Rollups

- [ ] `PL-ROLL-001` Headers-only counting — M5
- [ ] `PL-ROLL-002` Dimensions — M5
- [ ] `PL-ROLL-003` Storage — M5
- [ ] `PL-ROLL-004` Subject index — M5
- [ ] `PL-ROLL-005` Forecast — M5
- [ ] `PL-ROLL-006` Overview — M5
- [ ] `PL-ROLL-007` Subject tree — M5

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
- [ ] `PL-DB-005` Retention — M1 — ◐ partial: revocation pruning only; other tables arrive with their milestones
- [ ] `PL-DB-006` Permissions and placement — M1 — ◐ partial: 0700/0600 modes tested (TestDatabaseFileMode); Linux network-FS refusal implemented, untested
- [x] `PL-DB-007` Integrity — M1 — ✅ tested: internal/store TestCorruptDatabaseRefused

#### PL-UI — Dashboard

- [ ] `PL-UI-001` Stack — M0 — ◐ implemented: web/ builds with Vite 8 + router plugin + Mantine PostCSS (risk K-6 cleared); embedded via internal/ui; ECharts/Table/Virtual wired in M4/M5
- [ ] `PL-UI-002` Look — M0 — ◐ implemented: web/src/theme/theme.ts, collapsible sidebar, light/dark; no visual test yet (M4)
- [ ] `PL-UI-003` Shell — M4
- [ ] `PL-UI-004` Pages — M12
- [ ] `PL-UI-005` Typed API — M4
- [ ] `PL-UI-006` Charts — M4
- [ ] `PL-UI-007` Large lists — M4
- [ ] `PL-UI-008` Secret display — M4
- [ ] `PL-UI-009` Tests — M12
- [ ] `PL-UI-010` Missing UI — M0 — ◐ partial: `ui.FS()` returns nil without a build (internal/ui/ui_test.go); the serve-side notice lands with the admin listener in M3

#### PL-SDK — SDKs and CLI

- [ ] `PL-SDK-001` Go SDK — M2
- [ ] `PL-SDK-002` Python SDK — M13
- [ ] `PL-SDK-003` TS SDK — M13
- [ ] `PL-SDK-004` Conformance — M13
- [ ] `PL-SDK-005` Interop e2e — M13
- [ ] `PL-SDK-006` CLI — M2
- [ ] `PL-SDK-007` Inbox prefix — M2

#### PL-OPS — Packaging and operations

- [x] `PL-OPS-001` Image — M0 — ✅ tested (local): `make image` builds distroless static-debian13, digests pinned, user 65532:65532; CI image job implemented, not yet run on GitHub
- [ ] `PL-OPS-002` Commands — M0 — ◐ partial: `version`, `healthcheck`, `init`, `serve`, `keys export` implemented and tested; `banagent` (M8), `openapi` (M3) pending
- [x] `PL-OPS-003` Configuration — M1 — ✅ tested: internal/config tests; docs/operator/configuration.md
- [ ] `PL-OPS-004` Profiles — M1 — ◐ partial: prod refusals for admin exposure, plain master key, missing key tested (internal/config); plain-HTTP public listener and AOP checks arrive in M3/M8
- [ ] `PL-OPS-005` Dev stack — M1 — ◐ partial: deploy/compose.dev.yaml runs init → nats → pulsed (verified locally); SeaweedFS (M11) and pebble (M9) pending
- [ ] `PL-OPS-006` Reference prod compose — M1 — ◐ implemented: deploy/examples/compose.yaml; banagent service depends on M8
- [ ] `PL-OPS-007` CI — M0 — ◐ implemented: .github/workflows/ci.yaml (SHA-pinned, GOTOOLCHAIN=local); `make check` green locally; never run on GitHub (no remote)
- [ ] `PL-OPS-008` Release — M0 — ◐ implemented: .github/workflows/release.yaml; not externally verified (no remote, no tag)
- [ ] `PL-OPS-009` Memory budget — M2
- [ ] `PL-OPS-010` Graceful shutdown — M1 — ◐ partial: listeners and NATS drain on SIGTERM; rollup/consumer flushing arrives with M5
- [x] `PL-OPS-011` Versioning — M0 — ✅ tested: cmd/pulsed/main_test.go TestVersionJSON; docs/VERSIONING.md

#### PL-AUD — Audit

- [ ] `PL-AUD-001` Audit stream — M6
- [ ] `PL-AUD-002` Audit page — M12
- [ ] `PL-AUD-003` No secrets — M6

#### PL-TST — Testing

- [x] `PL-TST-001` Embedded server tests — M1 — ✅ tested: internal/testutil/natstest
- [x] `PL-TST-002` Race — M1 — ✅ tested: `make test` (-race) green
- [x] `PL-TST-003` Bootstrap tests — M1 — ✅ tested: internal/clients (creds connect, AUTH.ERR host, revoke → disconnect < 5 s, restart persistence, RELOAD new cert)
- [ ] `PL-TST-004` Query tests — M3
- [ ] `PL-TST-005` E2E — M4
- [ ] `PL-TST-006` Gate spike — M2
- [ ] `PL-TST-007` Restore drill — M11

#### PL-DOC — Documentation

- [ ] `PL-DOC-001` Operator docs — M12
- [ ] `PL-DOC-002` API docs — M3
- [ ] `PL-DOC-003` Envelope docs — M2
- [ ] `PL-DOC-004` Sizing — M2
- [ ] `PL-DOC-005` Backups — M11

## 7. Risks

| # | Risk | Check | Fallback |
|---|---|---|---|
| K-1 | NATS memory with a large file store and many subjects on 2 GB. | M2 spike (PL-TST-006). | Subject cardinality guideline; lower `max_bytes`; bigger VPS. |
| K-2 | Restored segments lose `Nats-Time-Stamp`. | M11 drill (PL-BAK-007). | Full snapshots only (ADR). |
| K-3 | Docker + nftables interplay, real client IP on IPv6. | M14 on the test VM. | nats with `network_mode: host`. |
| K-4 | CF free-plan limits (custom rules, one rate-limit rule with a 10 s period, one IP list). | M15 module design. | R-03. |
| K-5 | `cloudflare_api_token` needs a parent token with API Tokens: Edit. | M15 docs. | Create tokens manually. |
| K-6 | Vite 8 (rolldown) with the TanStack router plugin and the Mantine PostCSS preset. | M0 build. | Code-based routes. |
| K-7 | Huma SSE's per-message write deadline. | M3 tail tests. | Plain `http.Flusher` handler outside Huma, documented in the spec. |
| K-8 | Installing SDKs from git (pnpm `github:…#path:/sdk/ts`, pip `#subdirectory=`). | M13. | Publish to a private registry. |
| K-9 | SQLite single writer under bursty admin + rollup + delivery-log writes. | M5/M7 load tests. | Batch delivery-log writes. |

## 8. Commit workflow (local only)

1. `git status` and inspect the diff. Include only the milestone's files; no secrets.
2. `git add <explicit paths>`.
3. Generate the message with `/cc` (Conventional Commits). `/cc` prints a message only.
4. `git commit` with that message. 5. Report the hash. **Never push**, never create remotes, tags on remotes or PRs.
