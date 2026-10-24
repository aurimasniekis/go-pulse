# FAQ

### Is Pulse ready to use?

It is pre-release: no version is tagged yet. The server, dashboard, SDKs and CLI are implemented and tested locally,
including restore drills and cross-SDK interop. Not done yet: verification of the reference deployment on a real
server, and the sizing spike that measures NATS memory and replay latency with 10 GB of events on the 2 GB target box.
Read the [security model](/security/) before you rely on it.

### Why NATS JetStream and not Kafka, Redis Streams or a database?

JetStream gives durable streams, replay by time or sequence, per-subject filtering, durable consumers, an object store
and fine-grained per-user permissions in one small Go binary that runs comfortably on a 2 GB VPS. Kafka needs far more
memory; Redis Streams and databases would need the permission model and replay semantics built on top.

### How much history can it hold?

As much as fits in `EVENTS`' `max_bytes` (about 60% of the disk in the reference deployment). Events have no age
limit: when the stream is full, the oldest are discarded. The Overview shows the **retention horizon**, how many days
fit at the current daily volume, and the `disk_forecast` alert warns before the stream fills.

### Can a tool read events published by another tool?

Yes, if its grants cover those subjects. Grants are about subjects, not producers: a `subscribe` grant on `evt.home.>`
sees every home event, whoever published it. Producers cannot see who consumes their events.

### Is it exactly once?

No. Publishing is idempotent for two minutes per event ID (retries with the same ID are stored once). Consumption is at
least once: durable consumers and outbound webhooks redeliver until acknowledged. Make consumers idempotent on the
event ID.

### Can I backdate events?

You can set `ce-time` (when it happened) to any time. The stream timestamp (when it was stored) is always set by NATS,
and replay "since" uses it. So an import of old data is stored with today's stream timestamps and its original
`ce-time`.

### Why does my publish time out instead of saying "permission denied"?

NATS reports permission violations asynchronously on the connection. The JetStream call itself just never gets its
acknowledgement and times out. Check the client's grants.

### Why is there no login on the dashboard?

The dashboard binds to the server's loopback and is reached only through `ssh -L`, so SSH is the login. The admin
listener also checks the `Host` header (against DNS rebinding) and requires a CSRF header on writes. A single sign-on
mode (OIDC) is specified but not implemented, and `pulsed` refuses to expose the dashboard publicly without it.

### Can I run it without Cloudflare?

Not with the production profile as it is: the public listener requires an origin certificate and an Authenticated
Origin Pulls CA, and the client address logic trusts `CF-Connecting-IP` only from Cloudflare connections that passed
AOP. Raw NATS on 4222 does not depend on Cloudflare. For a private setup you can turn the public listener off
(`PULSE_PUBLIC_ADDR=off`); tools then use NATS only, without HTTP tokens, webhooks or `/ws`.

### Can I use it with plain NATS tools?

Yes, with a client's `.creds` and its inbox prefix: `nats --creds tool.creds --inbox-prefix _INBOX_<client-id> …`.
Events without the envelope headers are stored as untyped. See [Clients & SDKs](/clients/#plain-nats-clients).

### Does it scale out?

No. One NATS server, one `pulsed`, one VPS. That is a deliberate choice for a personal system; recovery from a lost
server is a [restore from backup](/operators/backups).

### Are events encrypted?

In transit, yes: TLS on 4222, and HTTPS through Cloudflare with mutual TLS to the origin. At rest inside NATS, no;
encrypt the disk if that matters. Backups are encrypted with age before they leave the server.

### What happens if I lose the master key?

`keys.enc` cannot be opened, so the NATS operator and account keys are gone. Every client needs new credentials, and
backups lose their keyed integrity checks (they still decrypt with the age identity). Keep the master key in your vault
and an offline copy. See [Keys and credentials](/operators/keys).

### How do I report a security problem?

Privately, through GitHub's
[private vulnerability reporting](https://github.com/aurimasniekis/go-pulse/security/advisories/new). Do not open a
public issue.
