# Configuration contract

> This file is the contract between the pulse image and the deployment repository (`events-wan-lt`).
> Every variable is `PULSE_*`. Secrets use a `*_FILE` variant pointing at a compose secret; in the prod profile the plain
> variable is refused for secrets. Variables marked with a milestone in parentheses are not implemented yet.
> Reference topology: [`deploy/examples/compose.yaml`](../../deploy/examples/compose.yaml).

## Commands

| Command | Purpose |
|---|---|
| `pulsed init` | One-shot, idempotent: keys (`keys.enc`), `operator.jwt`, `nats.conf`, bootstrap TLS leaf for 4222, banagent token. |
| `pulsed serve` | The server. |
| `pulsed healthcheck` | Container HEALTHCHECK: GET `PULSE_HEALTHCHECK_URL`, exit 0 when ready. |
| `pulsed keys export --yes-i-understand` | Print the decrypted key set (pulsed must be stopped; audited). |
| `pulsed version [--json]` | Build info. |
| `pulsed banagent` | nftables ban mirror (see [firewall.md](firewall.md)). |
| `pulsed openapi [--out-dir]` | Print or write the OpenAPI specs. |

## Core

| Variable | Default | Meaning |
|---|---|---|
| `PULSE_PROFILE` | `prod` | `dev` or `prod`. Prod refuses insecure settings (PL-OPS-004). |
| `PULSE_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error`. JSON logs on stdout. |
| `PULSE_DATA_DIR` | `/data/pulsed` | pulsed volume: `keys.enc`, `pulse.db`, `pulsed.lock`, `tls/ca.crt`, `banagent/token`, `acme/`. |
| `PULSE_NATS_DIR` | `/data/nats` | Volume shared with nats (mounted at the same path in both): `nats.conf`, `operator.jwt`, `tls/`, `jwt/`, `js/`. |
| `PULSE_MASTER_KEY_FILE` | — (required) | 32-byte master key, hex or base64. Never on a volume. `PULSE_MASTER_KEY` is accepted in dev only. |
| `PULSE_SHUTDOWN_TIMEOUT` | `20s` | Graceful shutdown bound. |

## NATS

| Variable | Default | Meaning |
|---|---|---|
| `PULSE_NATS_URL` | `tls://nats:4222` | pulsed's own connection. |
| `PULSE_NATS_TLS_NAME` | `nats` | Name verified on the 4222 certificate (internal CA leaf has `nats`, `localhost`, `127.0.0.1`, `::1`, raw host). |
| `PULSE_RAW_HOST` | — | Public raw NATS hostname (e.g. `r.events.wan.lt`); SAN on the bootstrap leaf and the ACME certificate name. |
| `PULSE_PUBLIC_HOST` | — | Public HTTPS hostname behind Cloudflare (e.g. `events.wan.lt`). |
| `PULSE_PUBLIC_NATS_URL` | `tls://<PULSE_RAW_HOST or localhost>:4222` | NATS URL shown in client snippets. |
| `PULSE_PUBLIC_URL` | `https://<PULSE_PUBLIC_HOST>` | Public HTTP base URL shown in token snippets. |
| `PULSE_NATS_CLIENT_LISTEN` | `0.0.0.0:4222` | Rendered into `nats.conf` by init. |
| `PULSE_NATS_MONITOR_LISTEN` | `0.0.0.0:8222` | NATS monitoring (never publish it). |
| `PULSE_NATS_WEBSOCKET_LISTEN` | `0.0.0.0:8080` | NATS websocket inside the compose network (pulsed proxies `/ws`, M8). Empty disables. |
| `PULSE_NATS_WEBSOCKET_URL` | `http://nats:8080` | Where pulsed proxies `/ws` (M8). |
| `PULSE_NATS_MAX_FILE_STORE` | NATS default | JetStream file store limit (`30GiB`, `60G`, bytes). |
| `PULSE_EVENTS_MAX_BYTES` | `10GiB` | `EVENTS` stream `max_bytes` (M2). Deployment computes 60% of the disk. |

## Listeners

| Variable | Default | Meaning |
|---|---|---|
| `PULSE_PUBLIC_ADDR` | `:8443` | Public listener. `off` disables it. Prod requires the origin certificate below. |
| `PULSE_ORIGIN_CERT_FILE` / `PULSE_ORIGIN_KEY_FILE` | — | Public listener TLS (Cloudflare Origin CA cert + key, PEM). Required in prod when the listener is on. Reload and AOP: M8. |
| `PULSE_ADMIN_ADDR` | `127.0.0.1:8080` | Admin UI + API. |
| `PULSE_AOP_CA_FILE` | — | Client CA for Authenticated Origin Pulls (Terraform output `aop_ca_certificate_pem`). Required in prod with a public listener. |
| `PULSE_BAN_THRESHOLD` / `PULSE_BAN_WINDOW` | `5` / `10m` | Failures within the window that trigger a ban. |
| `PULSE_BAN_DURATION` / `PULSE_BAN_MAX` | `1h` / `720h` | First ban length; repeat offences ×4 up to the max. |
| `PULSE_BAN_ALLOW` | — | Comma-separated IPs/CIDRs never banned. |
| `PULSE_HOST_IPS` | — | The VPS's own public addresses (never banned). |
| `PULSE_CF_RANGES_REFRESH` | `true` | Refresh Cloudflare ranges daily from api.cloudflare.com (the embedded list is the fallback). |
| `PULSE_CF_ACCOUNT_ID`, `PULSE_CF_BANLIST_ID`, `PULSE_CF_BANLIST_TOKEN_FILE` | — | Optional: mirror bans ≥ 1 h into a Cloudflare IP list (Terraform outputs `ban_list_id`, `ban_sync_api_token`). |
| `PULSE_OUTBOUND_ALLOW` | — | Comma-separated IPs/CIDRs outbound webhooks may reach although they are internal (e.g. a LAN service). |
| `PULSE_ADMIN_HOSTS` | — | Extra comma-separated Host names the admin listener accepts (besides 127.0.0.1, localhost, ::1). |
| `PULSE_ADMIN_PUBLISHED_LOOPBACK` | `false` | Set `true` when the admin port binds inside a container and compose publishes it only on host loopback. Required in prod for a non-loopback `PULSE_ADMIN_ADDR` with auth none (ADR-0008). |
| `PULSE_ADMIN_PUBLIC` | `false` | Mount the admin API on the public listener. Refused with auth none in prod. |
| `PULSE_AUTH_MODE` | `none` | `none`; `oidc` refuses to start until implemented (R-02). |
| `PULSE_METRICS_ADDR` | `127.0.0.1:9464` | `/metrics` (Prometheus), `/healthz`, `/readyz`, `/internal/v1/bans` (banagent). |
| `PULSE_HEALTHCHECK_URL` | `http://127.0.0.1:9464/readyz` | Probed by `pulsed healthcheck`. |

## ACME (Let's Encrypt for raw NATS, PL-EDGE-006, ADR-0032)

| Variable | Default | Meaning |
|---|---|---|
| `PULSE_ACME_ENABLED` | `false` | Obtain and renew a certificate for `PULSE_RAW_HOST` with DNS-01. Needs a real (non-localhost) raw host. |
| `PULSE_ACME_DIRECTORY` | Let's Encrypt production | ACME directory URL. Use `https://acme-staging-v02.api.letsencrypt.org/directory` for a first dry run. |
| `PULSE_ACME_EMAIL` | — | Optional account contact. |
| `PULSE_ACME_DNS_PROVIDER` | `cloudflare` | `cloudflare`, or `challtestsrv` (pebble's test DNS; refused in prod). |
| `PULSE_ACME_CF_TOKEN_FILE` | — | Cloudflare API token with Zone.DNS edit + Zone read on the zone (Terraform output `acme_dns_api_token`). Required for `cloudflare`. |
| `PULSE_ACME_CHALLTESTSRV_URL` | — | challtestsrv management URL (dev/tests). |
| `PULSE_ACME_DNS_RESOLVERS` | system | Comma-separated `host:port` resolvers lego uses for its own propagation checks. |
| `PULSE_ACME_CA_FILE` | — | Extra roots for the ACME directory's HTTPS (pebble's minica, `deploy/dev/pebble-minica.pem`). |
| `PULSE_ACME_NATS_ADDR` | host of `PULSE_NATS_URL` | `host:port` where pulsed checks the served certificate after a reload. |

The certificate and key are written to `<PULSE_NATS_DIR>/tls/acme.crt|key`, so pulsed needs the NATS volume mounted
read-write (as in the reference compose). Status and expiry appear on the System page.

## Alerts and notifiers (PL-OBS-004..006, ADR-0033)

| Variable | Default | Meaning |
|---|---|---|
| `PULSE_ALERT_INTERVAL` | `1m` | Rule evaluation interval (≥ 5 s). |
| `PULSE_ALERT_REPEAT` | `24h` | Re-notify while an alert keeps firing; `off` disables reminders. |
| `PULSE_TELEGRAM_BOT_TOKEN_FILE` + `PULSE_TELEGRAM_CHAT_ID` | — | Telegram notifier (Bot API `sendMessage`). Both or neither. |
| `PULSE_TELEGRAM_MIN_SEVERITY` | `warning` | `info`, `warning` or `critical`. |
| `PULSE_TELEGRAM_API_URL` | `https://api.telegram.org` | Override for tests or a Bot API proxy. |
| `PULSE_DISCORD_WEBHOOK_URL_FILE` | — | Discord notifier (incoming webhook URL; `https://` in prod). Mentions are disabled. |
| `PULSE_DISCORD_MIN_SEVERITY` | `warning` | `info`, `warning` or `critical`. |

Notifiers send at most 20 (Telegram) / 25 (Discord) messages per minute with a small burst; excess messages are counted
as suppressed and the next delivered message says how many were dropped. Tokens and webhook URLs never appear in logs
or API errors. Rules are edited on the Alerts page; their settings live in SQLite.

## Files the deployment provides

| Secret / file | Used by | Notes |
|---|---|---|
| master key | init, pulsed | compose secret, uid 65532 readable, mode 0400. |
| origin certificate + key | pulsed | `PULSE_ORIGIN_CERT_FILE`/`PULSE_ORIGIN_KEY_FILE`; reloaded when the files change. |
| AOP CA certificate | pulsed | `PULSE_AOP_CA_FILE`. |
| Cloudflare ban-list token | pulsed | optional, `PULSE_CF_BANLIST_TOKEN_FILE`. |
| Cloudflare ACME DNS token | pulsed | with ACME, `PULSE_ACME_CF_TOKEN_FILE`. |
| Telegram bot token, Discord webhook URL | pulsed | optional, `PULSE_TELEGRAM_BOT_TOKEN_FILE`, `PULSE_DISCORD_WEBHOOK_URL_FILE`. |
| Alloy config | alloy | `deploy/examples/config.alloy` (scrapes `127.0.0.1:9464` and the host; no remote_write). |

More secrets (S3, age recipients) are added with M11.
