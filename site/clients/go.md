# Go SDK

Package [`github.com/aurimasniekis/go-pulse/pkg/pulse`](https://github.com/aurimasniekis/go-pulse/tree/main/pkg/pulse),
on `nats.go` and its `jetstream` package. It is the SDK the CLI and `pulsed` itself use.

```sh
go get github.com/aurimasniekis/go-pulse/pkg/pulse
```

The module needs Go 1.27.

## Connect

```go
c, err := pulse.Connect("tls://r.events.example.com:4222",
	pulse.WithCredsFile("door-bot.creds"),
	pulse.WithSource("door-bot"))
if err != nil {
	log.Fatal(err)
}
defer c.Close() // drains: pending publishes and acks finish first
```

| Option | Effect |
|---|---|
| `WithCredsFile(path)` / `WithCreds(b)` | The client's `.creds` (required). The client ID inside sets the inbox prefix `_INBOX_<client-id>`. |
| `WithSource(name)` | `ce-source` for published events; also the connection name unless `WithName` is set. |
| `WithName(name)` | NATS connection name, shown on the client's page in the dashboard. |
| `WithCAFile(path)` / `WithCA(pem)` | Trust an extra CA, added to the system roots. Needed only while 4222 still serves the internal bootstrap certificate. |
| `WithStream(name)` | Default stream for subscriptions (`EVENTS`). |
| `WithNATSOptions(opts...)` | Extra `nats.go` options, e.g. `nats.DisconnectErrHandler`. |

Connections reconnect forever, once a second. `c.Conn()` returns the `*nats.Conn` and `c.JetStream()` the
`jetstream.JetStream` for anything the SDK does not wrap; `c.ClientID()` returns the client ID.

## Publish

```go
ack, err := c.Publish(ctx, "evt.home.door.opened", "home.door.opened.v1", map[string]any{"door": "front"})
// ack.ID (the event ID), ack.Stream, ack.Sequence, ack.Duplicate
```

`data` may be a JSON-marshalable value (`application/json`), a `string` (`text/plain; charset=utf-8`), `[]byte`
(`application/octet-stream`), `json.RawMessage` or `nil`. Pass `""` as the type for an untyped event.

| Option | Effect |
|---|---|
| `WithID(id)` | Event ID / `Nats-Msg-Id` (default: a new ULID). Reuse it on retries: duplicates within 2 minutes are dropped. |
| `WithTime(t)` | `ce-time` (default: now). |
| `WithContentType(ct)` | Override the content type. |
| `WithEventSource(s)` | Override `ce-source` for this event. |
| `WithHeader(k, v)` | Add an extra header (repeatable). |
| `WithBlob(ref)` | Attach a blob reference from `PutBlob`. |

`Publish` validates the subject before sending; it returns an error wrapping `envelope.ErrSubject` for a bad one.

## Subscribe

```go
type Door struct {
	Door string `json:"door"`
}

sub, err := c.Subscribe(ctx, "evt.home.door.>", pulse.Handle(func(ctx context.Context, e pulse.Event, d Door) error {
	log.Printf("%d %s %s door=%s", e.Sequence, e.StreamTime.Format(time.RFC3339), e.Type, d.Door)
	return nil
}), pulse.Since(time.Now().Add(-24*time.Hour)))
if err != nil {
	log.Fatal(err)
}
defer sub.Stop()
```

A `Handler` is `func(ctx context.Context, e pulse.Event) error`. `pulse.Handle[T]` decodes the JSON payload into `T`
first. The filter must be under `evt.>` when subscribing to `EVENTS`.

| Option | Effect |
|---|---|
| (none) | Deliver everything matching the filter from the start of the stream. |
| `Since(t)` | Replay from a stream timestamp. |
| `FromSeq(n)` | Replay from a stream sequence. |
| `OnlyNew()` | Only events published after subscribing. |
| `Durable(name)` | A durable consumer `<client-id>_<name>` (needs a `durable` grant). |
| `AckWait(d)` | Durable only: time before an unacknowledged event is redelivered (default 30 s). |
| `MaxDeliver(n)` | Durable only: limit redeliveries (default unlimited). |
| `OnError(fn)` | Receives handler errors, decode failures and consumer errors. Default: ignored. |
| `FromStream(name)` | Subscribe to another stream than the client's default (the grant must name it). |

**Without `Durable`**, the subscription is an ordered consumer: in order, no acknowledgements, recreated automatically
after a reconnect. Nothing is kept on the server when the tool stops; the next start replays from wherever you ask.

**With `Durable`**, the consumer and its position live on the server:

- a handler returning `nil` acknowledges the event;
- an error naks it for redelivery with a growing delay (1 s, 2 s, 4 s… up to 5 minutes);
- `pulse.Terminal(err)` (and decode failures from `Handle`) terminates it: no redelivery, reported to `OnError`;
- `Since`, `FromSeq` and `OnlyNew` only apply the first time the consumer is created. Later subscriptions resume
  from the stored position.

`sub.Stop()` stops delivery; a durable keeps its position. `sub.Closed()` is closed once it has fully stopped, and
`sub.Consumer()` returns the JetStream consumer name.

## The Event

| Field | Content |
|---|---|
| `Subject`, `ID`, `Type`, `Source`, `Time`, `ContentType` | The envelope. `Type == ""` (or `e.Untyped()`) for untyped events. |
| `Data` | Payload bytes. |
| `Blob` | `*envelope.BlobRef` when the payload is in the object store. |
| `Extra` | Other headers (`nats.Header`). |
| `Stream`, `Sequence`, `StreamTime` | Where and when NATS stored it. |

## Request/reply

```go
reply, err := c.Request(ctx, "svc.weather.now", []byte(`{"city":"Vilnius"}`))   // needs a request grant
```

Serving requests needs a `reply` grant on the subject; use the plain connection:

```go
_, err := c.Conn().Subscribe("svc.weather.now", func(m *nats.Msg) { _ = m.Respond([]byte(`{"temp":7}`)) })
```

The HTTP request bridge (`POST /api/v1/request/{subject}`) reaches the same services.

## Blobs

```go
ref, err := c.PutBlob(ctx, f, ".jpg")            // needs a blobs grant; unique name, size, SHA-256
_, err = c.Publish(ctx, "evt.home.camera.snapshot", "home.camera.snapshot.v1", nil, pulse.WithBlob(*ref))
data, err := c.GetBlob(ctx, *e.Blob)             // checks the SHA-256
```

## Alerts as events

`pulsed` publishes alert transitions on `_sys.alert.<rule>` as core NATS messages (not stored). With a `subscribe`
grant on `_sys.alert.>`:

```go
_, err := c.Conn().Subscribe("_sys.alert.>", func(m *nats.Msg) {
	e := envelope.Decode(m.Subject, m.Header, m.Data) // ce-type pulse.alert.firing.v1 or pulse.alert.resolved.v1
	log.Printf("%s %s", e.Type, e.Data)
})
```

See [Monitoring and alerts](/operators/monitoring) for the payload.
