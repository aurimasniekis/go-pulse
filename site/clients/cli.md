# pulse CLI

`pulse` publishes, subscribes and replays over NATS with a client's `.creds`, and manages clients, streams and
backups through the admin API tunnel. It is built on the [Go SDK](/clients/go).

```sh
go install github.com/aurimasniekis/go-pulse/cmd/pulse@latest
pulse --version
```

Or from a checkout: `make build-go` puts `pulse` and `pulsed` in `bin/`.

## Contexts

A context is a named NATS URL plus paths to the `.creds` file, an optional CA and the admin URL. Contexts live in
`<user config dir>/pulse/config.json` (mode 0600; `~/.config/pulse/` on Linux, `~/Library/Application Support/pulse/`
on macOS). Set `PULSE_CONFIG` to use another file. The file holds paths only, never secrets.

```sh
pulse context add home --url tls://r.events.example.com:4222 --creds ~/.config/pulse/door-bot.creds
pulse context add lab --url tls://localhost:4222 --creds ./demo.creds --ca ./pulse-dev-ca.pem \
  --admin-url http://127.0.0.1:8080 --admin-token-file ~/.config/pulse/lab-admin.token
pulse context ls          # * marks the current one
pulse context use lab
pulse context rm lab      # the creds file is left alone
```

| `context add` flag | Meaning |
|---|---|
| `--url` | NATS URL (required) |
| `--creds` | Path to the `.creds` file (required; stored as an absolute path) |
| `--ca` | Extra CA PEM, while NATS still serves the internal bootstrap certificate |
| `--admin-url` | Admin API base URL through the SSH tunnel, for the admin commands |
| `--admin-token-file` | File holding a personal access token for the admin commands (stored as an absolute path) |

The first context you add becomes the current one. Commands pick the context from `--context`, then `$PULSE_CONTEXT`,
then the current one.

## Event commands

These connect to NATS. They take the connection flags `--context`, `--url`, `--creds`, `--ca` (each overrides the
context) and `--source` (`ce-source` for published events, default `pulse-cli`).

### pub

```sh
pulse pub <subject> [data|-]
pulse pub evt.home.door.opened '{"door":"front"}' -t home.door.opened.v1
echo 'hello' | pulse pub evt.test.note.added - -t test.note.added.v1
# published evt.home.door.opened seq=1042 id=01J… (duplicate)
```

| Flag | Meaning |
|---|---|
| `-t`, `--type` | `ce-type`; empty publishes an untyped event |
| `--id` | Event ID / `Nats-Msg-Id` (default: a new ULID). Re-running with the same ID within 2 minutes prints `(duplicate)`. |
| `--content-type` | Default: detected (valid JSON → `application/json`, UTF-8 → `text/plain; charset=utf-8`, else octet-stream) |
| `-H`, `--header key=value` | Extra header, repeatable |

`-` reads the payload from stdin (up to 1 MiB).

### sub

```sh
pulse sub <filter>
pulse sub 'evt.home.>' --since 2026-01-01
pulse sub 'evt.github.>' --since 24h --json
pulse sub 'evt.home.>' --durable indexer
```

| Flag | Meaning |
|---|---|
| `--since` | Replay from a time: RFC 3339, `YYYY-MM-DD` (also with `THH:MM[:SS]`, local time), or a duration (`90m`, `24h`, `7d`, `2w`) |
| `--seq` | Replay from a stream sequence |
| `--new` | Only events published from now on |
| `--durable name` | Use the durable consumer `<client-id>_<name>` (needs a `durable` grant) |
| `--json` | One JSON object per event (`stream`, `seq`, `stream_time`, `subject`, `id`, `type`, `source`, `time`, `content_type`, `headers`, and `data`, `data_text` or `data_b64`) |
| `-n`, `--limit` | Exit after N events |

Without `--since`, `--seq` or `--new`, `sub` starts at the beginning of the stream. The default output is one line per
event: sequence, stream time, subject, type and the first 200 characters of the payload. Quote filters with `>` so the
shell does not treat it as a redirect.

### tail

```sh
pulse tail 'evt.home.>'      # same as: pulse sub 'evt.home.>' --new
```

### req

```sh
pulse req svc.weather.now '{"city":"Vilnius"}' --timeout 10s     # needs a request grant
```

Sends a NATS request and prints the reply. `-` reads the request body from stdin. `--timeout` defaults to 5 s.

## Admin commands

These call the admin API, which listens only on the server's loopback. Open the tunnel first:

```sh
ssh -L 8080:127.0.0.1:8080 <vps>
```

They authenticate with a **personal access token**: create one in the dashboard under **Account → Personal access
tokens**, with the role the commands need (`operator` or `administrator` to create clients, `viewer` to list). Tokens
work only through the tunnel, never on the public dashboard. Save it to a file with mode 0600.

They take `--context`, `--admin-url` (default: the context's admin URL, else `http://127.0.0.1:8080`) and
`--admin-token-file` (default: the context's token file, else the token in `$PULSE_ADMIN_TOKEN`).

### streams

```sh
pulse streams        # streams, KV and object stores in the APP account
```

### clients

```sh
pulse clients ls
pulse clients create door-bot --grant 'publish:evt.home.door.>' --grant 'subscribe:evt.home.>'
pulse clients create indexer --grant 'durable:evt.>:indexer' --out indexer.creds
pulse clients rotate door-bot -o door-bot.new.creds   # the old creds stop working; the file must not exist yet
pulse clients revoke door-bot --yes      # permanent
```

`--grant action[:subject[:name]]` is repeatable. Actions: `publish`, `subscribe`, `request`, `reply`, `durable` (with a
name) and `blobs` (no subject). Grants on other streams than `EVENTS` are set in the dashboard.

| `create` flag | Meaning |
|---|---|
| `--grant` | A grant, repeatable. Without any, the client can do nothing. |
| `--days` | Credential lifetime (default 365) |
| `-o`, `--out` | Where to write the `.creds` (default `<name>.creds`; `-` for stdout). Existing files are never overwritten. |
| `--description` | Description |

`rotate` takes `-o`/`--out` the same way. `rotate` and `revoke` accept the client's name or ID. Connected tools are
disconnected within seconds.

### backup

```sh
pulse backup ls
pulse backup run
pulse backup restore <backup-id> --identity backup-identity.txt --dry-run
pulse backup restore <backup-id> --identity backup-identity.txt --streams KV_settings
pulse backup restore <backup-id> --identity backup-identity.txt --streams EVENTS --replace
```

| `restore` flag | Meaning |
|---|---|
| `--identity` | The age identity file (required). It is sent to `pulsed` for this request only and never stored. |
| `--streams` | Streams to restore, comma-separated (default: all in the backup) |
| `--replace` | Delete existing streams first. Without it, streams that exist are skipped. |
| `--dry-run` | Show each stream's messages, sequence range and whether it exists, then stop |

`restore` waits for the restore job and prints the result per stream. Restoring `pulse.db` and `keys.enc` on a new
server is done with `pulsed backup restore --files`; see [Backup and restore](/operators/backups).
