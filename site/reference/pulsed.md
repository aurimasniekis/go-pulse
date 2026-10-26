# Server commands

`pulsed` is the server binary and the image's entrypoint: the compose services run `init`, `serve` and `banagent` from
the same image. Every command reads its configuration from the environment ([configuration](/reference/configuration)).
The tool-facing `pulse` CLI is documented separately ([pulse CLI](/clients/cli)).

| Command | Purpose |
|---|---|
| `pulsed init` | Create or complete the key set, operator JWT, `nats.conf`, bootstrap TLS and banagent token. Idempotent. |
| `pulsed serve` | Run the server. |
| `pulsed banagent` | Mirror bans into nftables. |
| `pulsed healthcheck` | Probe the readiness endpoint (container health check). |
| `pulsed backup keygen\|list\|run\|restore` | Backups. |
| `pulsed admin bootstrap\|recover\|token` | The first dashboard administrator; break-glass recovery; automation tokens. |
| `pulsed keys export` | Print the decrypted key set. |
| `pulsed openapi` | Print or write the OpenAPI specs. |
| `pulsed version` | Build information. |

Every command exits 0 on success and 1 on any error, which it prints on stderr.

## `pulsed init`

```sh
pulsed init
```

Runs once before NATS starts (the `init` compose service, `network_mode: none`). It loads the master key, creates or
loads the sealed `keys.enc`, writes `operator.jwt`, renders `nats.conf` from the `PULSE_NATS_*` settings, issues or
renews the internal TLS leaf for NATS, and writes the banagent token. A second run with nothing to do changes nothing.
With the wrong master key it fails instead of creating new keys.

## `pulsed serve`

```sh
pulsed serve
```

Connects to NATS, pushes the account JWTs if they are missing or stale, ensures the `EVENTS` stream and `blobs` store,
applies SQLite migrations, and starts the listeners (public, admin, metrics) and background work (rollups, alerts,
bans, outbound webhooks, ACME, backups). It shuts down gracefully on SIGINT or SIGTERM within `PULSE_SHUTDOWN_TIMEOUT`.
Only one `serve` can use a data directory at a time (`pulsed.lock`).

## `pulsed banagent`

```sh
pulsed banagent
```

Long-polls pulsed's ban feed and keeps the nftables ban sets in sync. Needs the host network and `NET_ADMIN`, but no
master key. Variables: `PULSE_BANAGENT_URL`, `PULSE_BANAGENT_TOKEN_FILE`, `PULSE_BANAGENT_TABLE`,
`PULSE_BANAGENT_SET_V4`, `PULSE_BANAGENT_SET_V6`, `PULSE_LOG_LEVEL` ([firewall and bans](/operators/bans#banagent)).

## `pulsed healthcheck`

```sh
pulsed healthcheck [--url URL] [--timeout 3s]
```

GETs the readiness URL and exits 0 on 200. The default URL is `PULSE_HEALTHCHECK_URL`, else
`http://127.0.0.1:9464/readyz`. It exists because the distroless image has no shell or curl.

## `pulsed backup`

`list` and `restore` need the backup configuration (`PULSE_BACKUP_ENABLED` and `PULSE_BACKUP_S3_*`) in the
environment; `keygen` needs nothing, and `run` only asks the running pulsed.

### `keygen`

```sh
pulsed backup keygen > backup-identity.txt
```

Prints a new age identity on stdout (keep it offline) and its recipient on stderr (`recipient: age1…`, for
`PULSE_BACKUP_AGE_RECIPIENTS`).

### `list`

```sh
pulsed backup list [--identity FILE]
```

Lists the complete backups in the object store: id, creation time, size and chunk count. With `--identity`, it also
decrypts each manifest and lists the streams with their message counts.

### `run`

```sh
pulsed backup run [--admin-url URL] [--token TOKEN]
```

Asks the running pulsed to start a backup now (`POST /api/admin/v1/backups/run`). The default admin URL is
`http://127.0.0.1:<port of PULSE_ADMIN_ADDR>`. It needs a personal access token with the operator or administrator
role (`--token`, or `PULSE_ADMIN_TOKEN`); create one under **Account → Personal access tokens**. Follow progress on the
Backups page or in the logs.

### `restore`

```sh
pulsed backup restore --id ID --identity FILE (--files | --all | --streams A,B) [--replace] [--overwrite] [--dry-run]
```

| Flag | Meaning |
|---|---|
| `--id` | Backup id (from `list`). Required. |
| `--identity` | age identity file. Required. |
| `--files` | Restore `pulse.db` and `keys.enc` into the data directory. pulsed must be stopped (the command takes the data-directory lock). |
| `--overwrite` | With `--files`: replace an existing `keys.enc`. `pulse.db` is always replaced. |
| `--all` | Restore every stream in the backup (NATS must be running; pulsed may be). |
| `--streams` | Restore only these streams. |
| `--replace` | Delete existing streams first. Without it, existing streams are skipped. |
| `--dry-run` | Show what would be restored. |

`--files` and `--all`/`--streams` are mutually exclusive. The disaster-recovery order is: `--files` before the first
start, then start the stack, then `--all` ([backup and restore](/operators/backups#new-server)).

## `pulsed admin`

Dashboard bootstrap, break-glass recovery and automation tokens ([Users and single sign-on](/operators/users)). They need the master key
(run them inside the pulsed container: `docker compose exec pulsed pulsed admin …`) and work while pulsed is running;
they then refuse a database whose schema differs from the binary's. Links are printed for `PULSE_ADMIN_URL` and shown
once; every command writes audit records.

```sh
pulsed admin bootstrap
pulsed admin recover --email you@example.com [--new-admin]
pulsed admin token --email automation@example.com [--create] [--role operator] [--days 1] [--name automation] [--cidr 203.0.113.7]
```

- **`bootstrap`** prints a one-time setup link (30 minutes) for creating the first administrator. It fails once an
  administrator exists.
- **`recover`** prints a single-use recovery login (30 minutes) for an existing user. Redeeming it signs out the user's
  sessions, removes their TOTP factor and recovery codes and offers a password reset. With `--new-admin` it creates a
  new administrator with that email instead.
- **`token`** prints a personal access token (stdout, once) for scripts, deployment checks and automation; it works on
  the loopback admin listener. With `--cidr` (repeatable, at least /16 or /48) it also works on the public dashboard
  host from those networks, when `PULSE_ADMIN_PUBLIC_TOKENS=true`. `--create` creates a missing user without a password (it can only use tokens); the role is
  added to the user's roles when missing. Anyone who can run it can already use `recover`, so it adds no new trust.

## `pulsed keys export`

```sh
pulsed keys export --yes-i-understand
```

Prints the decrypted key set (every signing key, in plaintext) as JSON on stdout. Refuses without
`--yes-i-understand`. pulsed must be stopped: the command takes the database lock and writes a `keys.export` audit
record before printing anything.

## `pulsed openapi`

```sh
pulsed openapi [--api admin|public] [--out-dir DIR]
```

Prints the OpenAPI 3.1 documents of the admin and public APIs, or writes `admin.openapi.json` and
`public.openapi.json` into `DIR`. Needs no configuration or running services. The repository's
[`api/`](https://github.com/aurimasniekis/go-pulse/tree/main/api) directory holds the generated files.

## `pulsed version`

```sh
pulsed version [--json]
```

Prints the version, commit and build date.
