# Subjects and the envelope

Every SDK, the CLI and the HTTP API produce the same message layout: a subject, a few headers (a "CloudEvents-lite"
envelope) and a payload. The Go package
[`pkg/pulse/envelope`](https://github.com/aurimasniekis/go-pulse/blob/main/pkg/pulse/envelope/envelope.go) is the
reference implementation, and every SDK is tested against the shared
[conformance vectors](https://github.com/aurimasniekis/go-pulse/blob/main/sdk/conformance/vectors.json).

## Subjects

```
evt.<domain>.<entity>.<action>[.<key>…]
```

- At least four tokens, starting with `evt`. Every token matches `[a-z0-9_-]+`: lowercase, no spaces, no dots.
- At most 256 bytes.
- Examples: `evt.home.door.opened`, `evt.home.door.opened.front`, `evt.github.repo.push.go-pulse`,
  `evt.hook.stripe.received`.

The SDKs, the CLI and the HTTP API reject other subjects before publishing. A raw `nats pub` to something under `evt.>`
is still stored, because NATS does not validate subjects; the dashboard marks it as non-conforming.

**Filters** for subscriptions use NATS wildcards: `*` for exactly one token, `>` for one or more trailing tokens:

| Filter | Matches |
|---|---|
| `evt.>` | everything |
| `evt.home.>` | every home event |
| `evt.*.door.*` | `evt.home.door.opened`, `evt.office.door.closed` (not `evt.home.door.opened.front`) |
| `evt.github.repo.push.go-pulse` | one subject |

### Choosing subjects

- Put **what happened** in the subject and **the details** in the payload. Consumers filter on subjects cheaply;
  payload filters (Explorer only) scan.
- Keep the number of distinct subjects modest. Until the sizing spike has been run, the guideline is the low tens of
  thousands per stream (the subject index stops at 50,000). Put high-cardinality IDs in the payload, or in a single
  trailing `<key>` token, not spread over several tokens.
- Version the **type**, not the subject: `evt.home.door.opened` with type `home.door.opened.v2`.

## Headers

| Header | Set by | Value |
|---|---|---|
| `Nats-Msg-Id` | SDKs (always) | A ULID. Also the idempotency key: a retry with the same ID within 2 minutes is stored once and acknowledged as a duplicate. |
| `ce-type` | you | Type with a version, e.g. `home.door.opened.v1`. Missing → the event is **untyped**. |
| `ce-source` | SDKs, from the connect option | The producing tool, e.g. `door-bot`. HTTP API default: `token:<name>`; webhooks: `hook:<name>`. |
| `ce-time` | SDKs (now, unless you pass a time) | When it happened: RFC 3339 in UTC with nanoseconds, trailing zeros trimmed, e.g. `2026-10-23T10:11:12.5Z`. |
| `content-type` | SDKs, from the data | `application/json`, `text/plain; charset=utf-8`, `application/octet-stream`, or what you pass. |
| `pulse-blob` | with a blob | `<bucket>/<object>` in the object store (bucket `blobs`). |
| `pulse-blob-size` | with a blob | Object size in bytes. |
| `pulse-blob-sha256` | with a blob | Hex SHA-256 of the object. |

`ce-specversion` is implied (`1.0`) and not sent. Header names are case-sensitive. Any other header you set is kept and
returned as an extra header.

### Two times

- `ce-time` is when the **producer** says the event happened. You can set it to a past time.
- The **stream timestamp** is when NATS **stored** the event. You cannot set it.

Replay "since" a time (`--since`, `Since`, `since=`) always uses the stream timestamp, so it is reliable even when
producers' clocks are wrong. Backups keep stream timestamps, so replay works the same after a restore.

## Payload

Anything up to 1 MiB (NATS `max_payload`). The SDKs pick the content type from the value you pass: bytes are
`application/octet-stream`, strings are `text/plain; charset=utf-8`, anything else is encoded as JSON. Pass a content
type to override it.

### Larger payloads: blobs

Payloads above 1 MiB go to the `blobs` object store, and the event carries a reference. The client needs a `blobs`
grant.

```go
ref, err := c.PutBlob(ctx, file, ".jpg")                          // stores it under a new unique name
_, err = c.Publish(ctx, "evt.home.camera.snapshot", "home.camera.snapshot.v1",
	map[string]any{"camera": "porch"}, pulse.WithBlob(*ref))
// consumer side: if e.Blob != nil { data, err := c.GetBlob(ctx, *e.Blob) }   // verifies the SHA-256
```

Python: `ref = await c.put_blob(data, ".jpg")`, `await c.publish(…, blob=ref)`, `await c.get_blob(e.blob)`.
TypeScript: `const ref = await c.putBlob(bytes, '.jpg')`, `c.publish(…, { blob: ref })`, `await c.getBlob(e.blob)`.

## Delivery guarantees

- **Publishing** is acknowledged by JetStream once the event is stored. Retrying with the same ID within two minutes
  is safe: the second copy is dropped and the ack says `duplicate`.
- **Consuming** is at least once. A durable consumer redelivers events that were not acknowledged in time; outbound
  webhooks retry. Make consumers idempotent on the event ID.
- **Ordering** is the stream order (sequence numbers) per consumer.
