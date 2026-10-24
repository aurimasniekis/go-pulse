"""PL-SDK-005 interop: replays what Go published and publishes for Go to read. Driven by internal/sdktest (make
test-sdk-interop); prints one JSON document on stdout."""

import argparse
import asyncio
import base64
import dataclasses
import hashlib
import json

from pulse import connect, format_time


def ev_json(e):
    return {"seq": e.sequence, "subject": e.subject, "id": e.id, "type": e.type, "source": e.source,
            "time": format_time(e.time_ns) if e.time_ns is not None else None, "content_type": e.content_type,
            "data_b64": base64.b64encode(e.data).decode(), "extra": e.extra,
            "blob": dataclasses.asdict(e.blob) if e.blob else None, "stream_time_ns": str(e.stream_time_ns)}


async def take(agen, n):
    out = []
    try:
        async for x in agen:
            if hasattr(x, "ack"):  # durable: settle before leaving the loop, or it is redelivered after ack_wait
                await x.ack()
            out.append(x)
            if len(out) == n:
                break
    finally:
        await agen.aclose()
    return out


async def main():
    p = argparse.ArgumentParser()
    p.add_argument("--url", required=True)
    p.add_argument("--creds", required=True)
    p.add_argument("--ca")
    p.add_argument("--since", required=True)
    p.add_argument("--count", type=int, required=True)
    a = p.parse_args()
    c = await connect(a.url, creds=a.creds, ca=a.ca, source="py-interop")
    out = {}
    events = await asyncio.wait_for(take(c.subscribe("evt.interop.>", since=a.since), a.count), 30)
    out["events"] = [ev_json(e) for e in events]
    for e in events:
        if e.blob:
            out["blob_sha256"] = hashlib.sha256(await c.get_blob(e.blob)).hexdigest()
    first = await asyncio.wait_for(take(c.subscribe("evt.interop.>", durable="py", since=a.since, ack_wait=2), 2), 30)
    again = await asyncio.wait_for(take(c.subscribe("evt.interop.>", durable="py", ack_wait=2), 1), 30)
    out["durable"] = [d.event.sequence for d in first + again]
    ref = await c.put_blob(b"python blob " * 20000, ".bin")
    ack = await c.publish("evt.interop.python.said", "interop.said.v1", {"lang": "python"}, blob=ref, headers={"x-lang": "py"})
    out["published"] = {"seq": ack.sequence, "id": ack.id, "blob": dataclasses.asdict(ref)}
    await c.close()
    print(json.dumps(out))


asyncio.run(main())
