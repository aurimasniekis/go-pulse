# Pulse — Architectural Decisions

> ADR log plus open review items. Format per ADR: **Status · Context · Decision · Alternatives · Consequences**.
> Statuses: `accepted`, `proposed`, `superseded by ADR-NNNN`, `withdrawn`. ADR numbers are never reused.
> A decision that weakens a GOAL.md security requirement must be labeled **security-relevant change** and needs operator review.

## 1. Decision log

### ADR-0001 — Product shape: `go-pulse`, `pulsed` server, `pulse` CLI, two repositories
- **Status:** accepted (M0, 2026-10-23)
- **Context:** All personal tools need one secure, durable pub/sub. The product must be reusable and versioned separately from
  the one VPS that runs it.
- **Decision:** Module `github.com/aurimasniekis/go-pulse` holds the product: `pulsed` (server, init, banagent), `pulse` (CLI),
  the embedded React UI, the Go/Python/TS SDKs, the Dockerfile, CI and the release workflow to `ghcr.io/aurimasniekis/pulsed`
  (private). Env prefix `PULSE_`. The deployment repo `events-wan-lt` holds Ansible, the prod compose with pinned image digests,
  the Alloy config, the vault, inventories, the Terraform Cloudflare module and runbooks. The interface between them is the
  image tag/digest, `docs/operator/configuration.md` and `deploy/examples/compose.yaml`.
- **Alternatives:** one repo for product and infra (mixes secrets/inventories with product code); hosted brokers (cost, lock-in,
  no control over retention).
- **Consequences:** The deployment repo pins digests and renders compose from the documented contract. Contract changes need a
  CHANGELOG entry and a minor/major version bump.

### ADR-0002 — NATS 2.15 JetStream is the only store
- **Status:** superseded by ADR-0024 (user decision during M0, 2026-10-23)
- **Context:** The system must stay small on a 2 vCPU / 2 GB VPS. A second database doubles backups, migrations and memory.
- **Decision:** NATS 2.15.1 (official image) with JetStream holds events (`EVENTS`), internal state (KV buckets in the PULSE
  account), the audit/alert log (`SYSLOG` stream), outbound delivery logs and blobs (Object Store). Charts come from rollups
  that pulsed writes into KV. No SQLite, no Postgres.
- **Alternatives:** SQLite for metadata (extra backup path, migrations, file locking in containers); Postgres (memory);
  Kafka/Redpanda (memory, operations).
- **Consequences:** Queries that a SQL store would answer (top subjects, time series) need purpose-built rollups and an index.
  KV CAS gives optimistic concurrency. Schema evolution is handled by versioned JSON records and a `meta` bucket.

### ADR-0003 — EVENTS stream configuration
- **Status:** accepted
- **Decision:** `EVENTS` on `evt.>`: file storage, `max_age` 0 (forever), `max_msgs` −1, `max_bytes` = configured value
  (deployment computes 60% of the data disk), `discard: old`, `compression: s2`, `duplicate_window: 2m`, `allow_direct: true`,
  1 replica, `max_msg_size` 1 MiB, `max_consumers` 256 (set at creation only; NATS cannot change it later — this is the
  plan's "default_max_consumers 256", which is a stream setting, not a server option). Extra user streams are created from the dashboard and may not overlap `evt.>` or `_sys.>`.
- **Alternatives:** `max_age` of 2 years (would drop history even with free disk); `discard: new` (stops ingest when full).
- **Consequences:** History is bounded by disk, not time. The retention horizon (ADR-0021) tells the operator how many days fit.
  Lowering `max_bytes` deletes old data and therefore needs explicit confirmation.

### ADR-0004 — Subject grammar `evt.<domain>.<entity>.<action>[.<key>]`
- **Status:** accepted
- **Decision:** ≥ 4 tokens, each `[a-z0-9_-]+`, lowercase. Gateway, SDK and CLI validate before publish. Raw `nats pub` is not
  validated by NATS and is accepted; non-conforming subjects are flagged in the UI.
- **Consequences:** Prefix rollups at depths 1–4 are meaningful (`evt.domain`, `evt.domain.entity`, …).

### ADR-0005 — CloudEvents-lite envelope in NATS headers
- **Status:** accepted
- **Decision:** Headers `Nats-Msg-Id` (ULID), `ce-type`, `ce-source`, `ce-time`, `content-type`. The payload is opaque and up to
  1 MB; larger blobs go to the `blobs` Object Store and the event carries a reference (`pulse-blob`). Events without `ce-type`
  are "untyped". `ce-specversion` is implied. ULIDs make IDs sortable and usable as idempotency keys.
- **Alternatives:** structured-mode CloudEvents JSON (forces JSON payloads, costs bytes per event); protobuf (tooling).
- **Consequences:** Rollups can count types from headers alone (`HeadersOnly` consumers).

### ADR-0006 — JWT operator mode, full resolver, accounts SYS/APP
- **Status:** accepted
- **Decision:** pulsed is the operator. Keys: operator + operator signing key; SYS and APP accounts each with a signing key.
  `nats.conf` preloads only SYS; APP is pushed by pulsed with `$SYS.REQ.CLAIMS.UPDATE` (full resolver, `allow_delete`).
  APP holds all tool clients, `EVENTS`, user streams and the `blobs` Object Store. pulsed holds one internal user in SYS and one
  in APP. (The planned PULSE account was dropped by ADR-0024 because pulsed's state moved to SQLite.)
  User JWTs for tool clients default to a 365-day expiry. Revocation uses the account JWT's `revocations` map.
- **Alternatives:** static `accounts {}` config with nkeys (reload to change, no revocation without reload); auth callout (more
  moving parts; a pulsed outage blocks all logins).
- **Consequences:** NATS keeps authenticating even when pulsed is down. pulsed must re-push accounts after key changes.

### ADR-0007 — Least-privilege grants and the permission generator
- **Status:** accepted
- **Decision:** New clients get no permissions besides their inbox. Grants are `{action, subject}`; the generator
  (`internal/natsauth`) derives pub/sub allow lists, a per-client inbox `_INBOX_<id>.>`, filter-scoped
  `$JS.API.CONSUMER.CREATE.<stream>.*.<filter>`, consumer info/next/ack subjects, and fixed denies (stream admin,
  unfiltered `$JS.API.DIRECT.GET.<stream>`, `$JS.API.STREAM.MSG.GET.>`, `$KV.>`, `$SYS.>`, and `$O.>` unless a `blobs` grant exists).
  NATS validates that a consumer's filter matches the filter token in the create subject, which makes the scoping sound.
  **NATS treats an empty allow list as "allow everything not denied"**, so a client without publish grants gets a single
  sentinel allow entry (`_pulse.no_publish_grants`) that matches nothing useful (found and tested at M1). Subscribe permissions
  always contain the client's inbox, so they are never empty.
- **Consequences:** Durable consumer names cannot be namespaced by token wildcards; see THREAT_MODEL.md T-12.

### ADR-0008 — Dashboard auth `none` behind an Authenticator seam, loopback only — **security-relevant**
- **Status:** accepted; **operator review: accepted by the operator in planning**
- **Context:** Central SSO does not exist yet. The dashboard can mint credentials and must not be reachable.
- **Decision:** `auth.mode=none` now, with an `Authenticator` interface; an `oidc` stub refuses to start. Admin UI and
  `/api/admin/v1` bind only to host loopback (`127.0.0.1:8080` published on the host loopback) and are reached through
  `ssh -L 8080:127.0.0.1:8080`. In the prod profile pulsed refuses `PULSE_ADMIN_PUBLIC=true` with auth none, and refuses a
  non-loopback admin address with auth none. When OIDC lands, `PULSE_ADMIN_PUBLIC=true` mounts the admin group on the public
  listener with a session cookie and a CSRF header.
- **Consequences:** Any local user/process on the VPS that can reach 127.0.0.1:8080 is an admin. The VPS is single-user.
  Within Docker, the admin listener binds `0.0.0.0:8080` inside the container network namespace and compose publishes it as
  `127.0.0.1:8080` only; pulsed checks the bind host in prod via `PULSE_ADMIN_PUBLISHED_LOOPBACK=true`, documented in the contract.

### ADR-0009 — No reverse proxy: pulsed terminates TLS with a CF Origin CA cert and AOP mTLS
- **Status:** accepted
- **Decision:** pulsed serves `:8443` (published as 443) with the Cloudflare Origin CA certificate and requires client certs
  chaining to the **module's own** AOP CA (per-hostname AOP with `tls_self_signed_cert` in Terraform), not Cloudflare's shared
  AOP CA, so other Cloudflare customers cannot reach the origin. The firewall allows 443 only from Cloudflare ranges. Public
  routes: `/api/v1/*`, `/hooks/*`, `/ws`, `/healthz`.
- **Alternatives:** Caddy/Traefik in front (another process, memory, config); Cloudflare Tunnel (cloudflared memory, and
  raw NATS still needs a direct port).
- **Consequences:** pulsed implements certificate reloading and the trusted-IP logic itself.

### ADR-0010 — Hostnames and raw NATS TLS via Let's Encrypt DNS-01
- **Status:** accepted
- **Decision:** `events.wan.lt` is proxied (orange cloud). `r.events.wan.lt` is DNS-only (grey cloud) for raw NATS on 4222 with a
  Let's Encrypt certificate that pulsed obtains itself with lego and the Cloudflare DNS-01 provider (scoped token:
  Zone.DNS edit on the zone). Until then, 4222 serves a bootstrap leaf from the internal CA. Renewal is 30 days before expiry.
  After writing files atomically, pulsed sends `$SYS.REQ.SERVER.<id>.RELOAD` and checks the served certificate serial.
- **Consequences:** Clients use system roots for `r.events.wan.lt`. During bootstrap, clients must trust the internal CA
  (`pulsed` exposes it in the UI).

### ADR-0011 — fail2ban-style bans in pulsed, enforced in nftables, HTTP and optionally Cloudflare
- **Status:** accepted
- **Decision:** Sources: NATS `CLIENT.AUTH.ERR` advisories, HTTP 401 on token routes, bad webhook signatures. Sliding window
  (default 5 in 10 min → 1 h), ×4 escalation up to 30 days. Bans in SQLite with an expiry (ADR-0024).
  Enforcement: HTTP middleware, `pulsed banagent` (host network, `NET_ADMIN`, polling pulsed's loopback
  `GET /internal/v1/bans` on the metrics listener with a bearer token from `banagent.token`) reconciling `pulse_ban_v4/v6` sets in the
  Ansible-owned `inet pulse_edge` table at `prerouting priority raw` (before Docker DNAT), and optionally a Cloudflare IP list
  for bans ≥ 1 h. Ansible's `nftables.conf` uses `destroy table inet pulse_edge`, never `flush ruleset`, so Docker's rules survive
  `systemctl restart nftables`. Host fail2ban handles SSH.
- **Consequences:** banagent needs `NET_ADMIN` and host networking; it does nothing else and holds only a read-only token for the
  ban list. If pulsed is down, banagent keeps the current set (element timeouts still expire bans).

### ADR-0012 — HTTP ingress: tokens, hooks, bridge, replay, SSE, outbound webhooks
- **Status:** accepted
- **Decision:** Public HTTP is for tools that cannot speak NATS. HTTP tokens are scoped by the same grant model as clients
  and stored hashed. Inbound hooks use pluggable verifiers (GitHub, Stripe, HMAC, shared secret). Outbound hooks are durable
  JetStream consumers with exponential backoff, HMAC signing and an SSRF-safe dialer; delivery logs live in SQLite (30 days).
- **Consequences:** The public listener's handlers are small and auditable; they publish into JetStream immediately.

### ADR-0013 — Huma v2 on net/http, OpenAPI 3.1 → openapi-typescript + openapi-fetch
- **Status:** accepted
- **Decision:** Two Huma APIs (public and admin) on separate `http.ServeMux`es. `pulsed openapi` emits both specs.
  The UI generates types with `openapi-typescript` and calls the API with `openapi-fetch`. Live data uses SSE.
  Huma SSE applies a per-message write deadline; long streams send keepalives well inside it.
- **Consequences:** CI regenerates `web/src/api/schema.d.ts` and fails on drift.

### ADR-0014 — UI: Mantine v8, React 19.3, Vite 8, TanStack, ECharts
- **Status:** accepted
- **Decision:** Mantine **v8** (8.3.x) as agreed in planning, although v9 exists, to keep the agreed stack and avoid a
  major-version migration during the build. TanStack Router (file routes via the Vite plugin), Query, Store, Table (v8, the stable
  API) and Virtual, with devtools in dev only. ECharts themed from Mantine CSS variables. Dense observability look:
  radius 4px, compact rows, Inter + JetBrains Mono (self-hosted via fontsource), grays + indigo, collapsible sidebar,
  Cmd-K (Mantine Spotlight), light/dark. Embedded with `//go:embed all:dist`; Vite `outDir: '../internal/ui/dist'`.
- **Consequences:** A later Mantine v9 upgrade is a separate ADR.

### ADR-0015 — Observability: slog, loopback metrics, Alloy without remote_write, alerts as events
- **Status:** accepted
- **Decision:** slog JSON. Prometheus `/metrics`, `/healthz`, `/readyz` on `127.0.0.1:9464` (separate listener). Alloy
  scrapes pulsed and `prometheus.exporter.unix`; no remote_write yet. Alerts are published as `_sys.alert.*` events and sent
  to Telegram and Discord.

### ADR-0016 — Backups: segments + snapshots, age, S3, GFS
- **Status:** accepted; restore drill gate pending (M11)
- **Decision:** Nightly. SQLite: online `VACUUM INTO` snapshot. Small streams and user KV/Object buckets: full snapshots via the JetStream snapshot API. `EVENTS`: append-only daily
  segments in the NATSARC1 format (read with batch direct get) plus a weekly full snapshot. age encryption, minio-go upload,
  GFS 7/4/12. If the drill shows that segment restore cannot preserve `Nats-Time-Stamp`, fall back to full snapshots only and
  record a superseding ADR.

### ADR-0017 — Sealed key store with XChaCha20-Poly1305 under the master key
- **Status:** accepted
- **Decision:** `keys.enc` = magic `PULSEKS1` ‖ version ‖ 24-byte nonce ‖ AEAD(JSON key set). The AEAD key is
  HKDF-SHA256(master key, salt = random 32 bytes stored in the header, info = `pulse keystore v1`). The header is AAD.
  `golang.org/x/crypto/chacha20poly1305` and the standard library `crypto/hkdf` only. The master key is a compose secret from ansible-vault and never
  stored on the data volume. Writes are atomic (temp file + fsync + rename) with mode 0600.
- **Alternatives:** age for the key store (fine, but a symmetric AEAD with a fixed header is simpler to reason about here).

### ADR-0018 — Runtime image: distroless static-debian13 nonroot
- **Status:** accepted
- **Decision:** `gcr.io/distroless/static-debian13:nonroot` pinned by digest, UID 65532, binary built with `CGO_ENABLED=0`.
  Healthcheck via `pulsed healthcheck` (no shell). Multi-arch amd64/arm64, cross-compiled without QEMU.

### ADR-0019 — Go 1.27.x with GOTOOLCHAIN=local in CI
- **Status:** accepted; amended at M0 (security fix)
- **Decision:** The plan named Go 1.27.2. At M0, govulncheck reported GO-2026-6809 (crypto/x509 distrust of compromised
  intermediates) in 1.27.2, fixed in 1.27.3, so `go.mod` declares `go 1.27.3`, the build image is `golang:1.27.3-trixie` and CI
  installs 1.27.3 with `GOTOOLCHAIN=local`. Later patch releases are adopted the same way when govulncheck requires it.

### ADR-0020 — Rollups via headers-only ordered consumers, stored in SQLite
- **Status:** accepted (storage amended by ADR-0024)
- **Decision:** One ordered `HeadersOnly` consumer per stream, resumed from a checkpoint row. Minute aggregates (totals,
  prefix depths 1–4, `ce-type`) flush every 10 s: one SQLite transaction upserts `rollup_1m`, `rollup_1h`, `rollup_1d`, the
  subject index and the checkpoint together. `rollup_1m` is pruned after 8 days. The last 48 h of minute buckets are cached in RAM.
- **Consequences:** Exactly-once counting (the checkpoint and the counts commit atomically), bounded memory, no payload reads.
- **Amendment (M5):** To bound row counts, each resolution keeps a subset of prefix depths: `rollup_1m` total, p1, p2,
  type (pruned after 8 days); `rollup_1h` total, p1–p3, type (pruned after 400 days); `rollup_1d` all dimensions including
  p4 (kept). Throughput charts switch to a coarser resolution when a deeper prefix is requested. Subject JSON keys are
  sampled from each subject's last message (direct get), at most 50 subjects per minute, so payload reads stay bounded.
  The minute cache is rebuilt from `rollup_1m` at start and fed by the consumers.

### ADR-0021 — Retention horizon and disk forecast from rollups
- **Status:** accepted
- **Decision:** horizon (days) = `max_bytes` ÷ average daily bytes over the last 30 days (or available days). Forecast date =
  now + (`max_bytes` − current bytes) ÷ average daily bytes, shown only while the stream is below `max_bytes`.

### ADR-0022 — Explorer engine
- **Status:** accepted
- **Decision:** Forward single-filter reads use batch direct get (`orbit.go/jetstreamext.GetBatch`) by sequence or start
  time. Multi-filter reads use an ordered consumer. Newest-first paging walks backward in sequence windows. Cursor =
  base64url(JSON{stream, dir, seq, filter hash}). Filters on headers, type and payload run in pulsed under budgets
  (50k msgs, 64 MB, 3 s, 4 concurrent queries).

### ADR-0023 — pulsed's own NATS connections use TLS with the internal CA plus system roots
- **Status:** accepted; the ACME sentence is amended by ADR-0032
- **Context:** 4222 requires TLS for every client. pulsed reaches NATS over the compose network as `nats`.
- **Decision:** The bootstrap leaf has SANs `nats`, `localhost`, `127.0.0.1` and the raw hostname. pulsed verifies with a pool
  of system roots + the internal CA and `ServerName = PULSE_NATS_TLS_NAME` (default `nats`). ~~After ACME replaces the leaf,
  pulsed sets the server name to the raw hostname automatically.~~ (ADR-0032: ACME adds a certificate, it does not replace
  the leaf.) No `InsecureSkipVerify` anywhere.

### ADR-0024 — pulsed state in SQLite; JetStream holds events only — supersedes ADR-0002
- **Status:** accepted (user decision during M0, 2026-10-23)
- **Context:** The user decided that the dashboard's database is SQLite, not JetStream KV. Relational queries (audit filters,
  delivery logs, top-N rollups, subject tree) are natural in SQL, and the operator can inspect one file.
- **Decision:** All pulsed-owned state lives in one SQLite database at `<PULSE_DATA_DIR>/pulse.db`: clients, grants, HTTP tokens,
  inbound/outbound hooks, the delivery log, bans/offences/allowlist, audit, alert rules and history, notifiers, settings,
  rollups, the subject index, rollup checkpoints and the backup catalog. JetStream keeps `EVENTS`, user streams and the `blobs`
  Object Store. Driver `modernc.org/sqlite` (pure Go, keeps `CGO_ENABLED=0` and distroless), WAL, `synchronous=FULL`, one writer
  connection plus `query_only` readers, embedded forward-only migrations (no ORM, hand-written queries with `database/sql`),
  an exclusive `flock` per data directory, `quick_check` at start, and no network I/O inside write transactions.
  Consequences for other decisions:
  - The PULSE account is dropped (ADR-0006): nothing internal is stored in NATS any more.
  - banagent reads bans from pulsed's loopback endpoint with a bearer token instead of watching KV (ADR-0011).
  - Rollup counts and checkpoints commit in one transaction (ADR-0020).
  - Backups add an online `VACUUM INTO` snapshot of the database (ADR-0016). Restore of the database happens offline
    (`pulsed restore`) or via a staged swap applied at the next start.
  - Audit records go to an `audit` table; alerts are still published as `_sys.alert.*` events in APP.
- **Alternatives:** JetStream KV for everything (ADR-0002, superseded); `mattn/go-sqlite3` (CGO, breaks distroless static);
  sqlc code generation (extra tooling for a small schema; may be adopted later).
- **Consequences:** pulsed is stateful on its own volume. The SQLite file and the NATS store must be backed up together for a
  consistent restore. SQLite is single-writer; pulsed batches high-frequency writes (rollups every 10 s).

### ADR-0025 — One scoped signing key per client carries its permissions
- **Status:** accepted (M0)
- **Context:** pulsed must not store client seeds (PL-AUTH-004), yet grant edits must not force operators to redistribute `.creds`.
- **Decision:** Each client gets its own scoped signing key in the APP account. The scope (`UserScope` template with role =
  client ID) holds the generated permissions. The user JWT is signed by that scoped key and carries no permissions itself. The
  scoped key's seed is discarded after signing. Grant edits rewrite the scope and re-push the APP account JWT. Revocation removes
  the scoped key (which invalidates every JWT it signed) and also adds the user key to `revocations`, then KICKs connections.
- **Alternatives:** permissions inside the user JWT (every grant edit needs new creds); auth callout (pulsed outage blocks logins).
- **Consequences:** The APP account JWT grows by roughly 0.5–2 KB per client; fine for hundreds of clients. Pushes are serialized
  in pulsed. Verified at M1 (`internal/clients/integration_test.go`): a scope change makes the server drop that client's
  live connections; auto-reconnecting clients come back with the new permissions and the same creds. User JWTs signed by a
  scoped key must carry a zero-valued permissions/limits section (jwt `HasEmptyPermissions`), not `NewUserClaims` defaults.

### ADR-0026 — API type generation runs in a separate tool package with TypeScript 5
- **Status:** accepted (M3)
- **Context:** The UI uses TypeScript 7 (native compiler). `openapi-typescript` 7.13 needs the TypeScript 5 compiler JS API,
  which TypeScript 7 no longer ships; pnpm overrides and packageExtensions cannot change peer resolution.
- **Decision:** `web/tools/apigen` is a separate pnpm package (own lockfile) pinning `openapi-typescript` 7.13.0 and
  `typescript` 5.9.3. `pnpm gen:api` / `make api` run it. The app keeps TypeScript 7. It is excluded from the Docker context.

### ADR-0027 — Admin listener Host allowlist (DNS-rebinding guard) — **security-relevant (strengthening)**
- **Status:** accepted (M3)
- **Decision:** The admin handler answers only requests whose `Host` is `127.0.0.1`, `localhost`, `::1` or listed in
  `PULSE_ADMIN_HOSTS`; others get 421. With auth none, loopback binding alone does not stop a malicious page that rebinds
  its DNS name to 127.0.0.1 while the operator's SSH tunnel is open (THREAT_MODEL T-21).

### ADR-0028 — Public listener in the prod profile requires TLS
- **Status:** accepted (M3)
- **Decision:** In prod, a non-empty `PULSE_PUBLIC_ADDR` requires `PULSE_ORIGIN_CERT_FILE`/`PULSE_ORIGIN_KEY_FILE`
  (PL-EDGE-010); `PULSE_PUBLIC_ADDR=off` disables the listener. M3 loads the certificate once; mtime reload and AOP mTLS
  arrive in M8.

### ADR-0029 — Webhook secrets sealed with the master key; delivery semantics
- **Status:** accepted (M7)
- **Decision:** Inbound verification and outbound signing need plaintext secrets, so they are stored sealed with the
  master key (secretbox, same scheme as the key store) instead of hashed. Outbound hooks deliver at least once, one event
  at a time in stream order (`MaxAckPending 1`), starting at events published after creation; failures back off from 1 s
  to 1 h over 12 attempts and then become dead letters (`Term`) so later events are not blocked forever. Manual retry
  re-delivers a logged event once without moving the consumer. The delivery log lives in SQLite for 30 days.
- **Consequences:** A copy of `pulse.db` without the master key reveals no webhook secrets. A permanently failing
  endpoint delays later events by about 2.3 hours per event until it recovers or the hook is paused.

### ADR-0030 — Verifier test vectors
- **Status:** accepted (M7)
- **Decision:** The GitHub verifier is tested against the vector in GitHub's "Validating webhook deliveries" docs (secret
  "It's a Secret to Everybody", payload "Hello, World!"). Stripe documents the scheme but publishes no fixed
  secret/payload vector, so Stripe tests use vectors computed exactly as documented (multiple `v1`, ignored `v0`,
  tolerance). Interoperability with live GitHub and Stripe deliveries is "externally verified" only after a real delivery.

### ADR-0031 — Ban semantics and enforcement plumbing — **security-relevant (strengthening)**
- **Status:** accepted (M8)
- **Decision:**
  - Ban key: IPv4 /32, IPv6 /64 (one host controls a /64); manual bans may name any CIDR.
  - Offence memory runs 30 days from the *end* of the last ban, so a 30-day ban does not reset its own escalation.
  - Failure windows and the "recent failures" list are in memory (bounded); bans, offences and the allowlist are in SQLite.
  - banagent long-polls `GET /internal/v1/bans` on the metrics listener with a bearer token from `banagent.token`
    and applies interval-set elements with timeouts through netlink (`github.com/google/nftables`).
  - The prod profile refuses a public listener without the AOP CA (in addition to the origin certificate).
  - The real IP is taken from `CF-Connecting-IP` only for Cloudflare peers that passed AOP.
  - The Cloudflare IP-list sync manages only items whose comment starts with `pulse:`.
- **Consequences:**
  - A pulsed restart forgets partial failure windows. That is accepted: an attacker gets at most `threshold − 1` extra attempts per restart.
  - Restarting nftables empties the sets until the next banagent poll (≤ 5 minutes, usually immediate).

### ADR-0032 — ACME certificate served next to the internal leaf, selected by SNI — amends ADR-0010, ADR-0023
- **Status:** accepted (M9)
- **Context:** ADR-0023 planned to replace the bootstrap leaf with the Let's Encrypt certificate and switch pulsed's
  server name to the raw hostname. That couples pulsed's own connection to public DNS and to the Let's Encrypt chain, and
  a failed renewal would also break pulsed.
- **Decision:**
  - `nats.conf` lists both pairs in `tls { certs: [...] }`: the ACME pair (`tls/acme.crt|key`) first, then the internal
    leaf. NATS (Go `crypto/tls`) picks the first certificate whose names match the client's SNI and falls back to the first
    entry. Tools on `r.events.wan.lt` get Let's Encrypt; pulsed keeps SNI `nats` and the internal CA.
  - pulsed runs lego with the account key from `keys.enc` (resolve by key, else register). It uses DNS-01 only:
    Cloudflare with a scoped token, or pebble's challtestsrv in dev and tests. The prod profile refuses challtestsrv.
  - Renewal starts at `min(30 days, lifetime / 3)` before expiry. For 90-day certificates that is 30 days; for the
    shorter Let's Encrypt profiles (45 and 6 days) it avoids renewing on every check. pulsed checks 10 s after start and
    then every 12 h, and retries after 1 h on failure.
  - Order of operations: write the key, then the certificate, both atomically. Re-render `nats.conf` (`bootstrap.Init`,
    idempotent), send `$SYS.REQ.SERVER.<id>.RELOAD`, then verify. Verification connects to the client port, reads INFO,
    upgrades to TLS with SNI = raw host and checks the served serial against the system roots plus the issuer
    certificates from the obtained bundle. It does not authenticate.
  - Status (serial, issuer, expiry, last attempt/success/error, verified) is in memory and shown on the System page with
    every other certificate (`GET /system/certificates`).
- **Consequences:**
  - A failed renewal leaves the old certificate served and pulsed's own connection unaffected.
  - Status history is lost on restart. The file on disk is the source of truth for serial and expiry; the M10 alert rule
    reads expiry from it.
  - The dev compose stack does not run pebble. ACME needs a non-localhost raw hostname. The pebble profile is the
    integration test `make test-acme` (Docker), which drives a real nats-server subprocess through obtain → install →
    reload → verify and renewal.

## 2. Open review items

| ID | Item | Default until decided |
|---|---|---|
| R-01 | License for the repository. | No LICENSE file. |
| R-02 | Central SSO provider and claims mapping for `auth.mode=oidc`. | `oidc` refuses to start. |
| R-03 | Cloudflare plan upgrade (more WAF rules, rate-limit periods, IP lists). | Free plan limits (see events-wan-lt). |
| R-04 | Off-site copy of age identities. | Operator keeps identities offline. |
