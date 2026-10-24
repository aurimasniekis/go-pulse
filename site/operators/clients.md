# Clients and grants

A **client** is one tool's identity: a NATS user with its own credentials (`.creds`) and a list of **grants**. Every
tool should get its own client. Revoking one tool then never affects another.

<Shot name="clients" themed alt="Clients page with grants, status, expiry and live connections" caption="Clients, their grants and live connections" />

## Least privilege by default

A new client has **no permissions**. It can connect, but it cannot publish, subscribe or replay anything until you add
grants. pulsed turns grants into NATS permissions inside the client's user JWT; NATS enforces them, not pulsed.

| Grant | Fields | Allows |
|---|---|---|
| `publish` | `subject` | Publishing to the subject pattern (JetStream acks come back on the client's inbox). |
| `subscribe` | `subject`, optional `stream` (default `EVENTS`) | Core subscriptions on the subject, plus replay through *ephemeral* consumers on that stream filtered to the subject, and direct gets on it. |
| `durable` | `subject`, `name`, optional `stream` | One named durable consumer, `<client-id>_<name>`, filtered to the subject. Create, read, delete and ack it. |
| `request` | `subject` | Sending requests to the subject. |
| `reply` | `subject` | Serving requests: subscribing to the subject and answering one reply per request. |
| `blobs` | — | Reading and writing the `blobs` Object Store (large payloads, see [the envelope](/clients/envelope)). |

Subjects use NATS wildcards (`evt.home.>`, `evt.*.door.*`). A grant subject must start with a literal token; `$…` and
`_INBOX…` subjects cannot be granted, and under `_sys` only `_sys.alert.>` (alert events) can be.

### What is always denied

Whatever the grants say, a client can never:

- publish to `$SYS.>` or `$KV.>`, or subscribe to them;
- create, update, delete, purge, snapshot or restore streams, or delete or read single stream messages by sequence;
- call account, server or meta JetStream APIs;
- create a durable consumer through the legacy `DURABLE.CREATE` API, or with a name its grants do not list;
- use the unfiltered direct get on a stream (it would bypass the subject filter);
- touch Object Stores (`$O.>`) without a `blobs` grant.

### Inbox prefix

Each client gets its own inbox prefix, `_INBOX_<client-id>.>`, instead of the shared `_INBOX.>`. Replies and JetStream
acks to one client are invisible to others. The SDKs read the prefix from the `.creds` file. With the `nats` CLI, pass
`--inbox-prefix _INBOX_<client-id>`, or every request ends in a permissions violation.

### Durable consumers

Durables are optional. Without a `durable` grant, SDKs replay through ordered ephemeral consumers, which is what most
tools need (`since 24h`, then live). Give a `durable` grant when a tool must resume exactly where it stopped and ack
each event. The consumer name is always prefixed with the client ID, so one client cannot squat another's name.

## Create a client

On the **Clients** page (operators and administrators): **New client**, a name, a description, tags and grants. Or
through the tunnel with a [personal access token](/operators/users#personal-access-tokens):

```sh
pulse clients create door-bot \
  --grant publish:evt.home.door.> \
  --grant subscribe:evt.home.> \
  --grant durable:evt.home.>:door-bot-worker \
  --out door-bot.creds
```

The grant format is `action[:subject[:name]]`. `--days` sets the lifetime (default 365). The `.creds` file is shown
**once**: pulsed stores the user's public key, never its seed. `pulse clients create` refuses to overwrite an existing
file and writes it with mode 0600.

<Shot name="client-creds" alt="The credentials dialog, shown once, with connection snippets" caption="Credentials are shown once, with snippets for Go, Python, TypeScript, curl and nats" />

## Change, rotate, revoke

| Action | Effect |
|---|---|
| Edit grants | New permissions take effect live. NATS reconnects the client's sessions; the creds stay the same. |
| **Rotate** (`pulse clients rotate <name>`) | New creds; the old user key is revoked. Connected sessions with the old creds are disconnected within seconds. |
| **Revoke** (`pulse clients revoke <name> --yes`) | Permanent. The user key is added to the APP account's revocation list and live connections are kicked. |
| **Kick** | Disconnects live connections now. The creds stay valid, so the tool reconnects. |

Credentials expire (365 days by default). The `client_expiry` alert fires 30 days before. An expired client shows as
**expired**: rotate it to issue new creds.

The client's page lists its live connections (address, uptime, RTT, messages, bytes, subscriptions) from the NATS server.

## HTTP tokens

For scripts, CI jobs and services that cannot hold a NATS connection, create an **HTTP token** on the **HTTP tokens**
page. Tokens use the same grant model (`publish`, `subscribe`, `request`) and work with the [HTTP API](/clients/http)
on the public hostname.

- Format `pulse_<prefix>_<secret>`. Only a hash is stored; the token is shown once. The prefix identifies it in the UI
  and the audit log.
- **Source networks**: limit a token to IPs or CIDRs (`198.51.100.7`, `203.0.113.0/24`). Empty means any.
- Tokens expire and can be revoked. Last use time and address are shown.
- A missing or invalid token gets 401 and counts toward a [ban](/operators/bans) for that address. A valid token without
  the grant gets 403 (no ban).

<Shot name="tokens" alt="HTTP tokens page with prefixes, grants, source networks and last use" caption="HTTP tokens" />

## Audit log

Every change made through the dashboard, the admin API or `pulsed keys export` is written to the audit log in SQLite:
client and token changes, grant edits, rotations, revocations, stream and bucket changes, webhook changes, bans,
backups and restores, alert rule edits and admin publishes. Secrets are never recorded.

The **Audit** page filters by action (exact, or a prefix such as `client.`), actor, target, result and time range.

<Shot name="audit" alt="Audit log filtered by action prefix" caption="Audit log" />
