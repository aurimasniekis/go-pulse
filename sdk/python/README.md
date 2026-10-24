# pulse-sdk (Python)

Async Python SDK for Pulse (`docs/ENVELOPE.md`). Requires Python ≥ 3.10.

```sh
pip install "git+https://github.com/aurimasniekis/go-pulse#subdirectory=sdk/python"
```

```python
import asyncio
from pulse import connect

async def main():
    c = await connect("tls://r.events.example.com:4222", creds="door-bot.creds", source="door-bot")
    await c.publish("evt.home.door.opened", "home.door.opened.v1", {"door": "front"})
    async for e in c.subscribe("evt.home.>", since="24h"):  # ordered replay, then live
        print(e.sequence, e.subject, e.json())

asyncio.run(main())
```

- `connect(url, creds=path | creds_data=content, source=, ca=)`: the inbox prefix `_INBOX_<client-id>` comes from
  the creds, so scoped clients stay within their grants.
- `publish(subject, type, data, id=, time=, content_type=, headers=, blob=)`: `bytes` → octet-stream, `str` → text,
  anything else → JSON.
- `subscribe(filter, since="24h" | datetime | RFC 3339, seq=, new=, durable=)`: without `durable`, an ordered consumer;
  with `durable="name"`, the consumer `<client-id>_name` (needs a durable grant) yielding `Delivery` objects to
  `ack()` / `nak()` / `term()`.
- `request(subject, data)`, `put_blob(data, ext)`, `get_blob(ref)`.

Tests: `make sdk-test` (conformance vectors) and `make test-sdk-interop` (against a real server).
