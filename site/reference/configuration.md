# Configuration

pulsed is configured only through `PULSE_*` environment variables. This page lists every one (banagent's own are on
[firewall and bans](/operators/bans#banagent)). The contract between the image and a deployment is
[docs/operator/configuration.md](https://github.com/aurimasniekis/go-pulse/blob/main/docs/operator/configuration.md);
the [reference compose file](https://github.com/aurimasniekis/go-pulse/blob/main/deploy/examples/compose.yaml) shows a
complete production setup.

**Secrets** have a `*_FILE` variant that names a file (a compose secret). In the `prod` profile the plain variable is
refused for every secret; the file variant is the only way. `*_FILE` wins when both are set.

**Durations** use Go syntax (`90s`, `10m`, `1h`, `720h`). **Sizes** accept bytes or a suffix: `K`, `M`, `G`, `T`
(decimal) or `KiB`, `MiB`, `GiB`, `TiB` (binary). **Lists** are comma-separated.

## Core

| Variable | Default | Meaning |
|---|---|---|
| `PULSE_PROFILE` | `prod` | `dev` or `prod`. Prod refuses insecure settings (see [below](#prod-profile-refusals)). |
| `PULSE_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error`. JSON logs on stdout. |
| `PULSE_DATA_DIR` | `/data/pulsed` | pulsed's volume: `keys.enc`, `pulse.db`, `pulsed.lock`, `tls/ca.crt`, `banagent/token`, `tmp/` (backup staging). |
| `PULSE_NATS_DIR` | `/data/nats` | The volume shared with NATS, mounted at the same path in both: `nats.conf`, `operator.jwt`, `tls/`, `jwt/`, `js/`. |
| `PULSE_MASTER_KEY_FILE` | — (required) | 32-byte master key, hex or base64. Never on a volume. `PULSE_MASTER_KEY` works in `dev` only. |
| `PULSE_SHUTDOWN_TIMEOUT` | `20s` | Bound for a graceful shutdown. |

## NATS

| Variable | Default | Meaning |
|---|---|---|
| `PULSE_NATS_URL` | `tls://nats:4222` | pulsed's own connection to NATS. |
| `PULSE_NATS_TLS_NAME` | `nats` | Name pulsed verifies on the NATS certificate. The internal leaf has `nats`, `localhost`, `127.0.0.1`, `::1` and the raw host. |
| `PULSE_RAW_HOST` | — | Public raw NATS hostname, e.g. `r.events.example.com`. A name on the bootstrap leaf and the ACME certificate. |
| `PULSE_PUBLIC_HOST` | — | Public HTTPS hostname behind Cloudflare, e.g. `events.example.com`. |
| `PULSE_PUBLIC_NATS_URL` | `tls://<PULSE_RAW_HOST or localhost>:4222` | NATS URL shown in client snippets. |
| `PULSE_PUBLIC_URL` | `https://<PULSE_PUBLIC_HOST>` | Public HTTP base URL shown in token snippets. |
| `PULSE_NATS_CLIENT_LISTEN` | `0.0.0.0:4222` | Client listener, rendered into `nats.conf` by `init`. |
| `PULSE_NATS_MONITOR_LISTEN` | `0.0.0.0:8222` | NATS monitoring, rendered by `init`. Never publish it. |
| `PULSE_NATS_WEBSOCKET_LISTEN` | `0.0.0.0:8080` | NATS WebSocket listener inside the compose network, rendered by `init`. pulsed proxies `/ws` to it. |
| `PULSE_NATS_WEBSOCKET_URL` | `http://nats:8080` | Where pulsed proxies `/ws` to. |
| `PULSE_NATS_MAX_FILE_STORE` | NATS default | JetStream file store limit (`30GiB`, `60G`, bytes). |
| `PULSE_EVENTS_MAX_BYTES` | `10GiB` | `max_bytes` of the `EVENTS` stream (at least 64 MiB). Deployments use about 60% of the data disk. Increases apply at start; decreases are not applied ([streams](/operators/streams#the-events-stream)). |

## Listeners

| Variable | Default | Meaning |
|---|---|---|
| `PULSE_PUBLIC_ADDR` | `:8443` | Public listener: `/api/v1`, `/hooks/*`, `/ws` (and the dashboard with `PULSE_ADMIN_PUBLIC`). `off` disables it. |
| `PULSE_ORIGIN_CERT_FILE` / `PULSE_ORIGIN_KEY_FILE` | — | Public listener TLS: the Cloudflare Origin CA certificate and key (PEM). Both or neither. Reloaded when the files change. |
| `PULSE_AOP_CA_FILE` | — | Client CA for Authenticated Origin Pulls. Clients must present a certificate that chains to it. |
| `PULSE_ADMIN_ADDR` | `127.0.0.1:8080` | Admin listener: the dashboard and `/api/admin/v1`. |
| `PULSE_ADMIN_PUBLISHED_LOOPBACK` | `false` | Set `true` when the admin port binds inside a container and compose publishes it only on host loopback. Required in prod for a non-loopback `PULSE_ADMIN_ADDR`. |
| `PULSE_ADMIN_HOSTS` | — | Extra `Host` names the admin listener accepts, besides `127.0.0.1`, `localhost` and `::1`. |
| `PULSE_METRICS_ADDR` | `127.0.0.1:9464` | `/metrics`, `/healthz`, `/readyz`, `/internal/v1/bans`. |
| `PULSE_HEALTHCHECK_URL` | `http://127.0.0.1:9464/readyz` | What `pulsed healthcheck` probes. |

## Dashboard sign-in

Every dashboard and admin API request needs a signed-in user ([Users and single sign-on](/operators/users)).
`pulsed admin bootstrap` prints the first administrator's setup link. `PULSE_AUTH_MODE` was removed: pulsed refuses to
start when it is set.

| Variable | Default | Meaning |
|---|---|---|
| `PULSE_ADMIN_PUBLIC` | `false` | Also serve the dashboard on the public listener (Cloudflare, AOP). Needs the public listener. |
| `PULSE_ADMIN_PUBLIC_HOST` | `PULSE_PUBLIC_HOST` | The public dashboard's hostname. Its own hostname serves only the dashboard (and the API hostname only the API); the API hostname serves the dashboard on every path except `/api/v1`, `/hooks`, `/ws` and `/healthz`. |
| `PULSE_ADMIN_PUBLIC_TOKENS` | `false` | Also accept personal access tokens on the public dashboard host, each only from the networks it was created with (tokens without networks stay loopback-only). For automation such as Terraform in CI. Needs `PULSE_ADMIN_PUBLIC`. |
| `PULSE_ADMIN_URL` | `https://<PULSE_ADMIN_PUBLIC_HOST>` with `PULSE_ADMIN_PUBLIC`, else `http://127.0.0.1:8080` | The dashboard origin: SSO redirects to `<PULSE_ADMIN_URL>/auth/oidc/callback` (register exactly this) and `pulsed admin` prints links for it. An origin without a path. |
| `PULSE_SESSION_IDLE` | `30m` | Sign-out after inactivity (at least `1m`). |
| `PULSE_SESSION_MAX` | `12h` | Absolute session lifetime (between the idle timeout and `168h`). |
| `PULSE_REAUTH_WINDOW` | `5m` | Sensitive actions need a sign-in this recent (at least `1m`; at most `15m` in prod). |
| `PULSE_MFA_REQUIRED_ROLES` | `administrator,operator` | Password accounts holding one of these roles must enroll TOTP. `none` requires it for nobody. |

## Bans and the edge

| Variable | Default | Meaning |
|---|---|---|
| `PULSE_BAN_THRESHOLD` | `5` | Failures within the window that trigger a ban (at least 2). |
| `PULSE_BAN_WINDOW` | `10m` | Counting window (at least 1m). |
| `PULSE_BAN_DURATION` | `1h` | First ban (at least 1m); repeat offences ×4. |
| `PULSE_BAN_MAX` | `720h` | Longest automatic ban (at least the first ban). |
| `PULSE_BAN_ALLOW` | — | IPs or CIDRs never banned. |
| `PULSE_HOST_IPS` | — | The VPS's own public addresses, never banned. |
| `PULSE_CF_RANGES_REFRESH` | `true` | Refresh Cloudflare's ranges daily from api.cloudflare.com; the embedded list is the fallback. |
| `PULSE_CF_ACCOUNT_ID`, `PULSE_CF_BANLIST_ID`, `PULSE_CF_BANLIST_TOKEN_FILE` | — | Optional, all three together: mirror bans of 1 h or more into a Cloudflare account IP list. |
| `PULSE_OUTBOUND_ALLOW` | — | IPs or CIDRs outbound webhooks may reach although they are internal (e.g. a LAN service). |

banagent reads its own variables; see [firewall and bans](/operators/bans#banagent).

## ACME

Let's Encrypt for the raw NATS hostname ([TLS](/operators/tls#let-s-encrypt-for-raw-nats)).

| Variable | Default | Meaning |
|---|---|---|
| `PULSE_ACME_ENABLED` | `false` | Obtain and renew a certificate for `PULSE_RAW_HOST` with DNS-01. Needs a real (non-localhost) raw host. |
| `PULSE_ACME_DIRECTORY` | Let's Encrypt production | ACME directory URL. Staging: `https://acme-staging-v02.api.letsencrypt.org/directory`. |
| `PULSE_ACME_EMAIL` | — | Optional account contact. |
| `PULSE_ACME_DNS_PROVIDER` | `cloudflare` | `cloudflare`, or `challtestsrv` (pebble's test DNS server; refused in prod). |
| `PULSE_ACME_CF_TOKEN_FILE` | — | Cloudflare API token with Zone.DNS edit and Zone read on the zone. Required for `cloudflare`. |
| `PULSE_ACME_CHALLTESTSRV_URL` | — | challtestsrv management URL (development and tests). |
| `PULSE_ACME_DNS_RESOLVERS` | system | `host:port` resolvers for lego's own propagation checks. |
| `PULSE_ACME_CA_FILE` | — | Extra roots for the ACME directory's HTTPS (e.g. pebble's test CA). |
| `PULSE_ACME_NATS_ADDR` | host of `PULSE_NATS_URL` | `host:port` where pulsed checks the served certificate after a reload. |

The certificate and key are written to `<PULSE_NATS_DIR>/tls/acme.crt` and `acme.key`, so pulsed needs the NATS volume
mounted read-write.

## Alerts and notifiers

| Variable | Default | Meaning |
|---|---|---|
| `PULSE_ALERT_INTERVAL` | `1m` | Rule evaluation interval (at least 5s). |
| `PULSE_ALERT_REPEAT` | `24h` | Re-notify while an alert keeps firing; `off` or `0` disables reminders. |
| `PULSE_TELEGRAM_BOT_TOKEN_FILE` + `PULSE_TELEGRAM_CHAT_ID` | — | Telegram notifier (Bot API `sendMessage`). Both or neither. |
| `PULSE_TELEGRAM_MIN_SEVERITY` | `warning` | `info`, `warning` or `critical`. |
| `PULSE_TELEGRAM_API_URL` | `https://api.telegram.org` | Override for tests or a Bot API proxy. |
| `PULSE_DISCORD_WEBHOOK_URL_FILE` | — | Discord notifier: an incoming webhook URL (`https://` required in prod). |
| `PULSE_DISCORD_MIN_SEVERITY` | `warning` | `info`, `warning` or `critical`. |

## Backups

| Variable | Default | Meaning |
|---|---|---|
| `PULSE_BACKUP_ENABLED` | `false` | Nightly encrypted backups to S3 ([backup and restore](/operators/backups)). |
| `PULSE_BACKUP_AT` | `03:30` | Daily start time, `HH:MM` UTC. |
| `PULSE_BACKUP_KEEP_DAILY` / `_WEEKLY` / `_MONTHLY` | `7` / `4` / `12` | Retention. Keep at least 1 daily. |
| `PULSE_BACKUP_AGE_RECIPIENTS` or `_FILE` | — | age recipients (`age1…`, comma- or newline-separated) from `pulsed backup keygen`. Required: backups are always encrypted. These are public keys, so the plain variable is allowed in prod. |
| `PULSE_BACKUP_S3_ENDPOINT` | — | `host[:port]` of the S3-compatible service. |
| `PULSE_BACKUP_S3_BUCKET` | — | The bucket (must exist). |
| `PULSE_BACKUP_S3_PREFIX` | `pulse/` | Key prefix. |
| `PULSE_BACKUP_S3_REGION` | `us-east-1` | Region used for signing. |
| `PULSE_BACKUP_S3_LOOKUP` | `auto` | `auto`, `path` or `virtual` (host-style) addressing. |
| `PULSE_BACKUP_S3_ACCESS_KEY_FILE` / `_SECRET_KEY_FILE` | — | Credentials. Least privilege: list, get, put and delete on the prefix. |
| `PULSE_BACKUP_S3_CA_FILE` | — | Extra CA for a private S3 endpoint. |
| `PULSE_BACKUP_S3_INSECURE` | `false` | Plain HTTP to S3. Development only; refused in prod. |
| `PULSE_BACKUP_S3_CREATE_BUCKET` | `false` | Create the bucket if missing. Development only; refused in prod. |

## Prod profile refusals

With `PULSE_PROFILE=prod` (the default), pulsed refuses to start when:

- a secret is given as a plain variable instead of `*_FILE`;
- the admin listener is not on loopback and `PULSE_ADMIN_PUBLISHED_LOOPBACK` is not `true`;
- `PULSE_REAUTH_WINDOW` is longer than 15 minutes;
- `PULSE_ADMIN_URL` is plain `http://` and not the loopback tunnel;
- the public listener is on without an origin certificate (plain HTTP), or without `PULSE_AOP_CA_FILE`;
- ACME uses the `challtestsrv` test DNS provider;
- the Discord webhook URL is not `https://`;
- backups use `PULSE_BACKUP_S3_INSECURE` or `PULSE_BACKUP_S3_CREATE_BUCKET`.

In every profile it refuses a missing master key, `PULSE_AUTH_MODE` (removed), `PULSE_ADMIN_PUBLIC` without the public
listener or a hostname, an invalid `PULSE_ADMIN_URL`, unknown roles in `PULSE_MFA_REQUIRED_ROLES`, out-of-range session
settings, incomplete pairs (origin certificate and key, Telegram token and chat ID, the three Cloudflare ban-list
settings), ACME without a real raw host or its DNS token, backups without an endpoint, bucket, credentials or
recipients, and out-of-range ban, alert and size settings. The error names the variable.

## Command and client variables

These are read by individual commands and the `pulse` CLI, not by `pulsed serve`. `PULSE_HEALTHCHECK_URL` is under
[Listeners](#listeners).

| Variable | Read by | Meaning |
|---|---|---|
| `PULSE_ADMIN_TOKEN` | `pulsed backup run`, the `pulse` admin commands | Personal access token, when `--token` (pulsed) or `--admin-token-file` / the context's token file (pulse) is not given. |
| `PULSE_CONFIG` | `pulse` | Path of the CLI's context file (default `<user config dir>/pulse/config.json`, e.g. `~/.config/pulse/config.json`). |
| `PULSE_CONTEXT` | `pulse` | Context to use when `--context` is not given (default: the current context). |

banagent reads `PULSE_BANAGENT_*`; see [firewall and bans](/operators/bans#banagent).

## Files the deployment provides

| Secret or file | Used by | Variable |
|---|---|---|
| Master key | `init`, `pulsed` | `PULSE_MASTER_KEY_FILE`. Readable by uid 65532, mode 0400. |
| Cloudflare Origin CA certificate and key | `pulsed` | `PULSE_ORIGIN_CERT_FILE`, `PULSE_ORIGIN_KEY_FILE` |
| AOP client CA certificate | `pulsed` | `PULSE_AOP_CA_FILE` |
| Cloudflare ACME DNS token | `pulsed` (with ACME) | `PULSE_ACME_CF_TOKEN_FILE` |
| Cloudflare ban-list token | `pulsed` (optional) | `PULSE_CF_BANLIST_TOKEN_FILE` |
| Telegram bot token, Discord webhook URL | `pulsed` (optional) | `PULSE_TELEGRAM_BOT_TOKEN_FILE`, `PULSE_DISCORD_WEBHOOK_URL_FILE` |
| Backup S3 credentials | `pulsed` (with backups) | `PULSE_BACKUP_S3_ACCESS_KEY_FILE`, `PULSE_BACKUP_S3_SECRET_KEY_FILE` |
| Alloy config | `alloy` | [`deploy/examples/config.alloy`](https://github.com/aurimasniekis/go-pulse/blob/main/deploy/examples/config.alloy) |

The age **identity** never goes on the server; only its recipient does.
