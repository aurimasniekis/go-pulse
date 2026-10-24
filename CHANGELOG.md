# Changelog

All notable changes are documented here. Format: [Keep a Changelog](https://keepachangelog.com/en/1.1.0/);
versioning: [docs/VERSIONING.md](docs/VERSIONING.md).

## [Unreleased]

### Added
- M0 skeleton: product contract, plan, decisions, threat model, test strategy; `pulsed version|healthcheck`; embedded
  dashboard shell (Mantine 8, TanStack Router); distroless image; CI and release workflows.
- M1: `pulsed init` (sealed key store, operator mode `nats.conf`, bootstrap TLS, banagent token), `pulsed serve`
  (SQLite state, SYS/APP connections, APP account sync), client identities with per-client scoped signing keys,
  `pulsed keys export`, dev compose stack, reference prod compose.
- M2: envelope package and SDK conformance vectors, `EVENTS` and `blobs` ensured at start, Go SDK (`pkg/pulse`),
  `pulse context|pub|sub`, sizing spike tooling (`make spike`, operator-triggered).
- M3: admin API (system, streams, consumers, explorer, SSE tail) and public listener skeleton on Huma v2, Host
  allowlist and CSRF header on the admin listener, `pulsed openapi`, generated TypeScript client types.
- M4: dashboard Explorer, Live tail and Streams & consumers pages, NATS status in the header, Playwright smoke tests.
- M5: rollups (minute/hour/day counts by prefix and type, exactly once), subject index, disk forecast; Overview and
  Subjects pages; admin publish endpoint and "Publish event" in the Explorer.
- M6: HTTP tokens and the public ingress API (publish, batch, request bridge, replay, SSE tail); client, token and
  audit admin APIs; Clients, HTTP tokens and Audit pages with show-once secrets and connection snippets.
- M7: inbound webhooks (GitHub, Stripe, HMAC, shared secret) and outbound webhooks (ordered, retried, signed,
  SSRF-guarded) with a delivery log; Webhooks page.
- M8: automatic bans (NATS, HTTP, webhooks) enforced over HTTP, in nftables (`pulsed banagent`) and optionally in a
  Cloudflare IP list; origin TLS with hot reload and Authenticated Origin Pulls; Cloudflare-aware client IP; `/ws`
  proxy; Security page. Prod now requires `PULSE_AOP_CA_FILE` for the public listener.
- M9: Let's Encrypt for raw NATS (`PULSE_ACME_*`): DNS-01 via Cloudflare, served next to the internal leaf by SNI,
  verified after reload, renewed early; `GET /system/certificates`; System page with server, NATS, JetStream and
  certificate status; `make test-acme` (pebble).
- M10: Prometheus `/metrics` (pulsed, NATS, JetStream, streams, Go runtime); alert engine with built-in and custom
  threshold rules, `_sys.alert.<rule>` events and history; Telegram and Discord notifiers (`PULSE_TELEGRAM_*`,
  `PULSE_DISCORD_*`); Alerts page; System page with runtime, server stats and connections; Grafana Alloy in the dev
  stack and a reference config.
- M11: nightly age-encrypted backups to S3 (`PULSE_BACKUP_*`): SQLite, `keys.enc` and every stream as NATSARC1
  snapshots stored deduplicated; verification, GFS retention, authenticated restores from the Backups page and
  `pulsed backup keygen|list|run|restore`; SeaweedFS in the dev stack.
- M12: KV & Objects browser, Settings page (effective configuration, display time zone), audit time filters, and a
  Playwright check of every page.
- M13: Python SDK (`sdk/python`) and TypeScript SDK (`@pulse/sdk`, node and browser over `/ws`) with conformance and
  Go ↔ Python ↔ TS interop tests.
- `pulse` CLI: `tail`, `req`, and `streams`, `clients ls|create|rotate|revoke`, `backup ls|run|restore` through the
  admin API tunnel. Operator guides in `docs/operator/` (deployment, keys, TLS, bans, backups, upgrades,
  troubleshooting).

### Fixed
- Outbound webhook delivery errors no longer include the destination URL, which can carry the secret (Slack and
  Discord webhook paths, query tokens); they were stored in the delivery log and logged.
- Reference compose: banagent could not write nftables sets (added capabilities do not reach the image's non-root
  user); it now runs as root with only NET_ADMIN and DAC_READ_SEARCH. The firewall contract documents that Debian's
  nftables unit flushes the whole ruleset on stop.
- Admin API requests canceled by the browser return 499 instead of logging an internal error.
- Go SDK: re-subscribing a durable with a different start position failed with "deliver policy can not be updated";
  it now binds to the existing consumer.
- The e2e overview test no longer depends on data left by earlier runs.
- `make secrets` ignores the user's git config: a global `color.ui=always` made gitleaks parse no commits locally.
