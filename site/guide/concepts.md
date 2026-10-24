# Concepts

## Events and subjects

An **event** is one NATS message stored in JetStream. Its **subject** says what happened:

```
evt.<domain>.<entity>.<action>[.<key>…]
evt.home.door.opened
evt.home.door.opened.front
evt.github.repo.push.go-pulse
```

Subjects have at least four tokens of lowercase `[a-z0-9_-]`. Consumers subscribe with NATS wildcards: `*` matches
one token (`evt.*.door.*`), `>` matches the rest (`evt.home.>`).

Each event carries a small **envelope** in its headers, modelled on CloudEvents: an ID (a ULID, also used to
de-duplicate retries), a type such as `home.door.opened.v1`, the producing tool as the source, the time the producer
says it happened, and the content type. The payload is anything up to 1 MiB; larger payloads go to the object store
and the event carries a reference. Events without a type (for example a raw `nats pub`) are stored and shown as
**untyped**. See [Subjects and the envelope](/clients/envelope).

## Streams

| Stream | What it holds |
|---|---|
| `EVENTS` | Every event under `evt.>`. No age limit; when it reaches `max_bytes` (by default 60% of the disk in the reference deployment), the oldest events are discarded. S2 compression, two-minute de-duplication window. |
| `blobs` | The object store for payloads above 1 MiB. Needs a `blobs` grant. |
| User streams | Extra streams you create on the Streams page for subjects outside `evt.>`. |
| KV buckets, object stores | Created and browsed by the operator on the KV & Objects page. Tool clients cannot read KV buckets directly. |

All of them live in the **APP** account. NATS runs in JWT operator mode with a second, system account (`SYS`) that only
`pulsed` uses.

Two timestamps matter: `ce-time` is when the producer says the event happened; the **stream timestamp** is when NATS
stored it. Replay "since" a time always uses the stream timestamp.

## Clients and grants

A **client** is one tool's identity on NATS: a user JWT and seed in a `.creds` file. `pulsed` mints it and shows the
`.creds` **once**; it keeps the public key, never the seed.

A new client can do nothing until you add **grants**:

| Grant | Lets the client |
|---|---|
| `publish` on a subject pattern | publish events there (and receive the JetStream acknowledgement) |
| `subscribe` on a filter | replay and follow events matching the filter, through ordered consumers it creates itself |
| `durable` on a filter, with a name | keep a durable consumer named `<client-id>_<name>` whose position survives restarts |
| `request` on a subject | send NATS requests (request/reply services) |
| `reply` on a subject | serve requests on that subject |
| `blobs` | read and write the `blobs` object store |

Grants become NATS permissions on a per-client signing key, so editing grants never needs new `.creds`: connected
tools reconnect with the new permissions. Each client also gets its own inbox prefix (`_INBOX_<client-id>`), and stream
administration, unfiltered reads and KV access are always denied. See [Clients and grants](/operators/clients).

**Rotate** issues new `.creds` and revokes the old ones. **Revoke** is permanent. Either disconnects the tool within
seconds. Credentials expire after 365 days by default; an alert fires 30 days before.

## Other ways in

| Way in | For | Authenticated by |
|---|---|---|
| NATS on port 4222 (TLS) | tools with an SDK or the CLI | the client's `.creds` |
| HTTP API `/api/v1` | scripts and tools that only speak HTTP | an HTTP **token** with its own grants and optional source networks |
| Inbound webhooks `/hooks/<name>` | GitHub, Stripe, anything that signs with HMAC | the sender's signature or shared secret |
| WebSocket `/ws` | browsers | a client's `.creds`, over the same NATS protocol |

The HTTP routes are served on port 443 behind Cloudflare. Port 4222 is reached directly. Out of Pulse, **outbound
webhooks** deliver matching events to your URLs, signed with HMAC and retried for about two hours.

## The operator side

- **Dashboard and admin API**: bound to `127.0.0.1:8080` on the server and reached with `ssh -L`, or, when enabled, on
  its own hostname behind Cloudflare. Every request needs a signed-in **user** (password + TOTP, or single sign-on) with
  a **role**: administrator, operator, auditor or viewer. The CLI uses personal access tokens on the tunnel.
- **Rollups**: `pulsed` counts events per minute, hour and day by subject prefix and type, from headers only. They feed
  the Overview charts, the subject tree, the disk forecast and the retention horizon (how many days of history fit).
- **Bans**: repeated authentication failures (NATS, HTTP tokens, webhook signatures) from one address ban it, first for
  an hour, then longer. Bans are enforced in `pulsed`, in nftables and optionally in a Cloudflare IP list.
- **Audit log**: every admin change and security-relevant action.
- **Alerts**: built-in rules (disk forecast, certificate expiry, stale backups, ban storms…) and your own threshold
  rules, sent to Telegram or Discord and published as `_sys.alert.<rule>` events.
- **Backups**: the SQLite state, the sealed key set and every stream, nightly to S3, encrypted with age.

`pulsed`'s own state (clients, tokens, webhooks, bans, audit, alerts, rollups, settings, backup catalog) lives in one
SQLite file next to the sealed key set `keys.enc`. Events are never copied into SQLite.

## Keys you must keep

| Key | Why |
|---|---|
| The **master key** (32 bytes) | Unseals `keys.enc`, which holds the NATS operator and account keys. Without it, every client needs new credentials. |
| The **age backup identity** | Decrypts backups. It never goes on the server. |

See [Keys and credentials](/operators/keys).

Next: [Try it locally](/guide/try-locally).
