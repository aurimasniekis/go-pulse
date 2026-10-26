# Changelog

All notable changes are documented here. Format: [Keep a Changelog](https://keepachangelog.com/en/1.1.0/);
versioning: [docs/VERSIONING.md](docs/VERSIONING.md).

## [Unreleased]

### Added
- Personal access tokens can name source networks. With the new `PULSE_ADMIN_PUBLIC_TOKENS=true` (off by default; needs
  `PULSE_ADMIN_PUBLIC`), such a token also works on the public dashboard host, but only from those networks, so
  automation like Terraform in CI can reach the admin API without an SSH tunnel. Set networks on the Account page or
  with `pulsed admin token --cidr`; each must be at least /16 (IPv4) or /48 (IPv6). Tokens without networks stay
  loopback-only, and refused tokens on the public host count towards a ban.

### Security
- Opting in with `PULSE_ADMIN_PUBLIC_TOKENS` relaxes the earlier rule that personal access tokens work only on the
  loopback listener. A token with networks can be used by anyone who can send traffic from those networks; keep them
  narrow and prefer the operator role for automation.

## [0.1.2] - 2026-10-26

### Fixed
- The disk forecast no longer fires for a new or quiet stream. When the stream would take more than about 292 years to
  fill, the date overflowed to one in the past, so the alert reported "-106752.0 days" and the Overview showed a date
  in 1734. Forecasts beyond 100 years now have no date, like a stream with no traffic.
- The documented nftables edge rule (operator firewall guide and bans page) now matches only traffic addressed to the
  host (`fib daddr type local`). The old rule also dropped the containers' own outbound HTTPS, which broke ACME
  certificates, the Cloudflare ban-list sync and the Cloudflare range refresh.

## [0.1.1] - 2026-10-25

### Added
- The single sign-on groups claim can name a nested claim with a dotted path, such as `porch.groups` for Porch. A
  top-level claim with exactly that name still takes precedence, so names like `https://example.com/groups` keep
  working.

## [0.1.0] - 2026-10-25

First release. Pulse is a personal durable event bus on NATS JetStream: tools publish and subscribe with their own
least-privilege credentials, any tool can replay history from a timestamp, and one server with a dashboard manages
clients, webhooks, bans, alerts and backups. The image is `docker.io/aurimasniekis/pulsed:0.1.0` (also
`ghcr.io/aurimasniekis/pulsed:0.1.0`) for linux/amd64 and linux/arm64.

> **Not externally reviewed.** Nobody other than the author has reviewed the code, the key handling or the backup
> format.

### Known limitations

- Pre-1.0: interfaces may change between minor versions ([docs/VERSIONING.md](docs/VERSIONING.md)).
- The reference deployment (firewall, ban enforcement, IPv6 client addresses, host roles) was tested in containers,
  including a Debian 13 converge test, but not yet on a real VPS behind Cloudflare.
- The memory budget for the 2 vCPU / 2 GB target and the subject-cardinality guideline are provisional: the sizing
  spike (about 10 GB of events, 5,000 subjects) has not been run.
- Webhook verifiers are tested against documented vectors, not live GitHub or Stripe deliveries; single sign-on is
  tested against an in-process identity provider, not a live one such as Keycloak or Entra ID.
- A `subscribe` grant can pull from another client's consumer on the same stream if it knows the consumer's name.
- `EVENTS` cannot be shrunk from Pulse: a lower `PULSE_EVENTS_MAX_BYTES` is ignored with a warning.
- No visual regression tests for the dashboard; browser tests run in Chromium only.

### Added
- `pulsed version|healthcheck`; embedded dashboard shell (Mantine 8, TanStack Router); distroless image; CI and release
  workflows.
- `pulsed init` (sealed key store, operator mode `nats.conf`, bootstrap TLS, banagent token), `pulsed serve`
  (SQLite state, SYS/APP connections, APP account sync), client identities with per-client scoped signing keys,
  `pulsed keys export`, dev compose stack, reference prod compose.
- Envelope package and SDK conformance vectors, `EVENTS` and `blobs` ensured at start, Go SDK (`pkg/pulse`),
  `pulse context|pub|sub`, sizing spike tooling (`make spike`, operator-triggered).
- Admin API (system, streams, consumers, explorer, SSE tail) and public listener skeleton on Huma v2, Host
  allowlist and CSRF header on the admin listener, `pulsed openapi`, generated TypeScript client types.
- Dashboard Explorer, Live tail and Streams & consumers pages, NATS status in the header, Playwright smoke tests.
- Rollups (minute/hour/day counts by prefix and type, exactly once), subject index, disk forecast; Overview and
  Subjects pages; admin publish endpoint and "Publish event" in the Explorer.
- HTTP tokens and the public ingress API (publish, batch, request bridge, replay, SSE tail); client, token and
  audit admin APIs; Clients, HTTP tokens and Audit pages with show-once secrets and connection snippets.
- Inbound webhooks (GitHub, Stripe, HMAC, shared secret) and outbound webhooks (ordered, retried, signed,
  SSRF-guarded) with a delivery log; Webhooks page.
- Automatic bans (NATS, HTTP, webhooks) enforced over HTTP, in nftables (`pulsed banagent`) and optionally in a
  Cloudflare IP list; origin TLS with hot reload and Authenticated Origin Pulls; Cloudflare-aware client IP; `/ws`
  proxy; Security page. Prod now requires `PULSE_AOP_CA_FILE` for the public listener.
- Let's Encrypt for raw NATS (`PULSE_ACME_*`): DNS-01 via Cloudflare, served next to the internal leaf by SNI,
  verified after reload, renewed early; `GET /system/certificates`; System page with server, NATS, JetStream and
  certificate status; `make test-acme` (pebble).
- Prometheus `/metrics` (pulsed, NATS, JetStream, streams, Go runtime); alert engine with built-in and custom
  threshold rules, `_sys.alert.<rule>` events and history; Telegram and Discord notifiers (`PULSE_TELEGRAM_*`,
  `PULSE_DISCORD_*`); Alerts page; System page with runtime, server stats and connections; Grafana Alloy in the dev
  stack and a reference config.
- Nightly age-encrypted backups to S3 (`PULSE_BACKUP_*`): SQLite, `keys.enc` and every stream as NATSARC1
  snapshots stored deduplicated; verification, GFS retention, authenticated restores from the Backups page and
  `pulsed backup keygen|list|run|restore`; SeaweedFS in the dev stack.
- KV & Objects browser, Settings page (effective configuration, display time zone), audit time filters, and a
  Playwright check of every page.
- Python SDK (`sdk/python`) and TypeScript SDK (`@pulse/sdk`, node and browser over `/ws`) with conformance and
  Go ↔ Python ↔ TS interop tests.
- `pulse` CLI: `tail`, `req`, and `streams`, `clients ls|create|rotate|revoke`, `backup ls|run|restore` through the
  admin API tunnel. Operator guides in `docs/operator/` (deployment, keys, TLS, bans, backups, upgrades,
  troubleshooting).
- Apache-2.0 license, a README, `SECURITY.md` and `CONTRIBUTING.md`, and a documentation website built from
  `site/` (VitePress) and deployed to GitHub Pages, with screenshots from a throwaway stack (`make screenshots`).
- Dashboard users and single sign-on: local accounts with Argon2id passwords,
  mandatory TOTP for administrators and operators, recovery codes, invitations and password links; administrator,
  operator, auditor and viewer roles with a permission on every admin operation; server-side sessions with CSRF tokens
  and re-authentication for sensitive actions; OpenID Connect providers (PKCE, group → role mappings, just-in-time
  users, linking, test logins, SSO-only, step-up); personal access tokens for the CLI; `pulsed admin bootstrap|recover|token`;
  the dashboard optionally on the public listener on its own or the API hostname (`PULSE_ADMIN_PUBLIC`,
  `PULSE_ADMIN_PUBLIC_HOST`, `PULSE_ADMIN_URL`); `PULSE_SESSION_IDLE`, `PULSE_SESSION_MAX`, `PULSE_REAUTH_WINDOW`,
  `PULSE_MFA_REQUIRED_ROLES`; `pulse_dashboard_logins_total`; `make dev-admin`.
- Admin API endpoints for declarative tooling such as the
  [Terraform provider](https://github.com/aurimasniekis/terraform-provider-pulse): `DELETE /clients/{id}` and
  `DELETE /tokens/{id}` remove revoked (or, for tokens, expired) records so their names can be reused, keeping client
  revocations in force; `GET|PUT /streams/{name}/consumers/{consumer}` reads and creates or updates a durable pull
  consumer; `GET|PUT /kv/{bucket}/config` and `GET|PUT /objects/{bucket}/config` read and update a bucket's
  description, history, TTL and size limit. Consumers report `ack_wait_seconds`, `max_deliver`, `max_ack_pending`
  and the start position; KV buckets report `description` and `max_bytes`, object stores `max_bytes`.

### Security
- No anonymous dashboard access: every admin operation is authorized by role; failed dashboard sign-ins are
  throttled, audited and, on the public listener, count toward bans (`dashboard` source).
- Outbound webhook delivery errors never include the destination URL, which can carry a secret.

[Unreleased]: https://github.com/aurimasniekis/go-pulse/compare/v0.1.2...HEAD
[0.1.2]: https://github.com/aurimasniekis/go-pulse/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/aurimasniekis/go-pulse/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/aurimasniekis/go-pulse/releases/tag/v0.1.0
