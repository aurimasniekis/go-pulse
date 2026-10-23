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
| `PULSE_RAW_HOST` | — | Public raw NATS hostname (e.g. `r.events.wan.lt`); SAN on the bootstrap leaf, ACME name (M9). |
| `PULSE_PUBLIC_HOST` | — | Public HTTPS hostname behind Cloudflare (e.g. `events.wan.lt`). |
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
| `PULSE_ADMIN_HOSTS` | — | Extra comma-separated Host names the admin listener accepts (besides 127.0.0.1, localhost, ::1). |
| `PULSE_ADMIN_PUBLISHED_LOOPBACK` | `false` | Set `true` when the admin port binds inside a container and compose publishes it only on host loopback. Required in prod for a non-loopback `PULSE_ADMIN_ADDR` with auth none (ADR-0008). |
| `PULSE_ADMIN_PUBLIC` | `false` | Mount the admin API on the public listener. Refused with auth none in prod. |
| `PULSE_AUTH_MODE` | `none` | `none`; `oidc` refuses to start until implemented (R-02). |
| `PULSE_METRICS_ADDR` | `127.0.0.1:9464` | `/metrics` (M10), `/healthz`, `/readyz`, `/internal/v1/bans` (M8). |
| `PULSE_HEALTHCHECK_URL` | `http://127.0.0.1:9464/readyz` | Probed by `pulsed healthcheck`. |

## Files the deployment provides

| Secret / file | Used by | Notes |
|---|---|---|
| master key | init, pulsed | compose secret, uid 65532 readable, mode 0400. |

More secrets (Cloudflare origin cert/key, AOP CA, DNS token, S3, age recipients, notifier tokens) are added with M7–M11.
