# Event envelope and subjects

> PL-ENV-001..005, PL-BUS-002/003/004/009. Implementation: `pkg/pulse/envelope` (Go); conformance vectors:
> `sdk/conformance/vectors.json` (shared by every SDK).

## Subjects

```
evt.<domain>.<entity>.<action>[.<key>…]
```

- At least 4 tokens; every token matches `[a-z0-9_-]+` (lowercase). Max 256 bytes.
- Examples: `evt.home.door.opened`, `evt.home.door.opened.front`, `evt.github.repo.push.go-pulse`.
- SDKs, the CLI and the HTTP API reject other subjects before publishing. Raw `nats pub` to anything under `evt.>` is
  still stored (NATS does not validate) and shown as non-conforming in the UI.
- Subscription filters use NATS wildcards: `evt.home.>`, `evt.*.door.*`.
- Cardinality: see `docs/SIZING.md` (provisional guideline until the spike is run).

## Headers (CloudEvents-lite)

| Header | Required | Value |
|---|---|---|
| `Nats-Msg-Id` | yes (SDKs generate) | ULID. Also the idempotency key: a retry with the same ID within 2 minutes is stored once. |
| `ce-type` | for typed events | Reverse-DNS-ish type with a version, e.g. `home.door.opened.v1`. Missing → "untyped". |
| `ce-source` | recommended | The producing tool, e.g. `home-assistant`. |
| `ce-time` | yes (SDKs set now) | RFC 3339 with nanoseconds, UTC, trailing zeros trimmed: `2026-10-23T10:11:12.5Z`. |
| `content-type` | recommended | `application/json`, `text/plain; charset=utf-8`, `application/octet-stream`, … |
| `pulse-blob` | for large payloads | `<bucket>/<object>` in the Object Store (bucket `blobs`). |
| `pulse-blob-size` | with `pulse-blob` | Size in bytes. |
| `pulse-blob-sha256` | with `pulse-blob` | Hex SHA-256 of the object. |

`ce-specversion` is implied `1.0`. Header names are case-sensitive. Any other header is passed through as an extra header.
The stream timestamp (`Nats-Time-Stamp` / message metadata) is when NATS stored the event; `ce-time` is when the
producer says it happened. Replay `--since` uses the stream timestamp.

## Payload

Up to 1 MiB (NATS `max_payload`). Larger payloads go to the `blobs` Object Store (needs a `blobs` grant) and the event
carries the reference headers. In Go: `ref, _ := c.PutBlob(ctx, r, ".jpg")` then `c.Publish(…, pulse.WithBlob(*ref))`.

## SDK examples

Go:

```go
c, err := pulse.Connect("tls://r.events.example.com:4222", pulse.WithCredsFile("bot.creds"), pulse.WithSource("bot"))
ack, err := c.Publish(ctx, "evt.home.door.opened", "home.door.opened.v1", map[string]any{"door": "front"})
sub, err := c.Subscribe(ctx, "evt.home.>", pulse.Handle(func(ctx context.Context, e pulse.Event, d Door) error {
	return nil
}), pulse.Since(time.Now().Add(-24*time.Hour)))
```

CLI:

```sh
pulse context add home --url tls://r.events.example.com:4222 --creds ~/.config/pulse/bot.creds
pulse pub evt.home.door.opened '{"door":"front"}' -t home.door.opened.v1
pulse sub 'evt.home.>' --since 2026-01-01 --json
```

Python and TypeScript: see [sdk/python/README.md](../sdk/python/README.md) and [sdk/ts/README.md](../sdk/ts/README.md).

## Errors to expect

- A publish or subscribe outside the client's grants is rejected by NATS as a permission violation. NATS reports it
  asynchronously, so a denied JetStream call surfaces as a timeout (default 5 s) rather than an immediate error.
