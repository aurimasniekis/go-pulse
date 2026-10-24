# Python SDK

An asyncio SDK on [nats-py](https://github.com/nats-io/nats.py), in
[`sdk/python`](https://github.com/aurimasniekis/go-pulse/tree/main/sdk/python). Package `pulse`, distribution
`pulse-sdk`, Python 3.10 or later.

```sh
pip install "git+https://github.com/aurimasniekis/go-pulse#subdirectory=sdk/python"
```

## Example

```python
import asyncio
from pulse import connect

async def main():
    async with await connect("tls://r.events.example.com:4222", creds="door-bot.creds", source="door-bot") as c:
        ack = await c.publish("evt.home.door.opened", "home.door.opened.v1", {"door": "front"})
        print(ack.sequence, ack.id, ack.duplicate)

        async for e in c.subscribe("evt.home.>", since="24h"):   # ordered replay, then live
            print(e.sequence, e.subject, e.type, e.json())

asyncio.run(main())
```

## Connect

```python
c = await connect(url, creds="tool.creds")            # or creds_data="<.creds content>"
```

| Argument | Effect |
|---|---|
| `creds` / `creds_data` | Path to, or content of, the client's `.creds` (one is required). The client ID inside sets the inbox prefix `_INBOX_<client-id>`. |
| `source` | `ce-source` for published events; also the connection name unless `name` is set. |
| `name` | NATS connection name. |
| `ca` | Path to an extra CA file, needed only while 4222 serves the internal bootstrap certificate. `tls://` URLs always use TLS. |
| `stream` | Default stream for subscriptions (`EVENTS`). |
| `**nats_options` | Passed to `nats.connect`. |

The connection reconnects forever. `await c.close()` drains it; `Client` is an async context manager. `c.nc` and `c.js`
are the underlying nats-py connection and JetStream context.

## Publish

```python
ack = await c.publish(subject, type, data, id="", time=None, content_type="", source="", headers=None, blob=None)
```

- `data`: `bytes` → `application/octet-stream`, `str` → `text/plain; charset=utf-8`, `None` → empty, anything else is
  JSON-encoded.
- `type=""` publishes an untyped event. `id` is the event ID (default: a new ULID); reuse it on retries.
- `time` sets `ce-time`: a `datetime`, an RFC 3339 string or integer nanoseconds.
- Returns `PubAck(id, stream, sequence, duplicate)`. A bad subject raises `SubjectError`.

## Subscribe

```python
async for e in c.subscribe(filter, since=None, seq=None, new=False, durable=None, stream=None,
                           batch=100, ack_wait=30.0, max_deliver=-1):
    ...
```

- `since`: a duration (`"24h"`, `"7d"`, `"1h30m"`, `"2w"`), an RFC 3339 string, a `datetime` or integer
  nanoseconds. It replays by stream timestamp.
- `seq`: replay from a stream sequence. `new=True`: only new events.
- **Without `durable`**: an ordered consumer that yields `Event` objects in order and resumes after reconnects or gaps
  from the last delivered sequence. Nothing is kept on the server.
- **With `durable="name"`**: the consumer `<client-id>_name` (needs a `durable` grant) keeps its position on the server
  and yields `Delivery` objects. Call `await d.ack()`, `await d.nak(delay=None)` or `await d.term()`; a delivery still
  unsettled when the loop moves on is acknowledged. Deliveries unsettled when you leave the loop, and prefetched ones,
  come back after `ack_wait` seconds (at least once). The start options only apply when the durable is first created.

```python
async for d in c.subscribe("evt.home.>", durable="indexer", since="30d"):
    try:
        await index(d.event)
        await d.ack()
    except TemporaryError:
        await d.nak(delay=5)
```

### The Event

`subject`, `id`, `type`, `source`, `time_ns` (integer nanoseconds; `e.time` is a UTC `datetime` rounded to
microseconds), `content_type`, `data` (bytes), `blob`, `extra` (other headers), and the read side: `stream`,
`sequence`, `stream_time_ns`. Helpers: `e.json()`, `e.text()`, `e.untyped`.

Times are integers in nanoseconds because `ce-time` and stream timestamps carry nanoseconds that `datetime` would
round.

## Request/reply and blobs

```python
reply = await c.request("svc.weather.now", b'{"city":"Vilnius"}', timeout=5.0)   # needs a request grant

ref = await c.put_blob(jpeg_bytes, ".jpg")                                          # needs a blobs grant
await c.publish("evt.home.camera.snapshot", "home.camera.snapshot.v1", {"camera": "porch"}, blob=ref)
data = await c.get_blob(e.blob)                                                     # checks the SHA-256
```

Blobs use the NATS object store protocol over the granted subjects and are interchangeable with the Go and TypeScript
SDKs.

## Tests

`make sdk-test` runs the unit tests against the shared conformance vectors; `make test-sdk-interop` runs Go, Python and
TypeScript against a real server.
