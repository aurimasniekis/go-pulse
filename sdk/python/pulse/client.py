"""Async Pulse client on nats-py (PL-SDK-002): the same surface as the Go SDK, within a scoped client's grants."""

from __future__ import annotations

import asyncio
import base64
import hashlib
import json
import re
import ssl
import time as _time
from dataclasses import dataclass
from datetime import datetime, timedelta, timezone
from typing import Any, AsyncIterator, Dict, Optional, Union

import nats
from nats.aio.client import Client as NATS
from nats.aio.client import RawCredentials
from nats.js import api
from nats.js.errors import NotFoundError
from nats.errors import TimeoutError as NatsTimeout

from . import envelope
from .envelope import BlobRef, Event

DEFAULT_STREAM = "EVENTS"
BLOBS_BUCKET = "blobs"
_CLIENT_TAG = "pulse-client:"
_CHUNK = 128 * 1024


def client_id_from_creds(creds: str) -> str:
    """The Pulse client ID from the user JWT tags in .creds content (used for the inbox prefix, PL-SDK-007)."""
    m = re.search(r"-----BEGIN NATS USER JWT-----\s*([A-Za-z0-9_\-\.=]+)\s*------END NATS USER JWT------", creds)
    if not m:
        raise ValueError("pulse: creds contain no user JWT")
    payload = m.group(1).split(".")[1]
    claims = json.loads(base64.urlsafe_b64decode(payload + "=" * (-len(payload) % 4)))
    for t in claims.get("nats", {}).get("tags", []) or []:
        if t.startswith(_CLIENT_TAG):
            return t[len(_CLIENT_TAG):]
    raise ValueError("pulse: creds carry no pulse client tag")


@dataclass
class PubAck:
    id: str
    stream: str
    sequence: int
    duplicate: bool


_DUR = re.compile(r"(\d+)(ms|s|m|h|d|w)")
_UNIT = {"ms": 1e-3, "s": 1, "m": 60, "h": 3600, "d": 86400, "w": 604800}


def parse_since(since: Union[str, datetime, int, None]) -> Optional[int]:
    """'24h', '7d', '1h30m', an RFC 3339 time, a datetime or unix nanoseconds → unix nanoseconds."""
    if since is None:
        return None
    if isinstance(since, str):
        s = since.strip()
        if s and _DUR.sub("", s) == "":
            secs = sum(int(n) * _UNIT[u] for n, u in _DUR.findall(s))
            return _time.time_ns() - int(secs * 1e9)
    return envelope.to_ns(since)


def _rfc3339(ns: int) -> str:
    return datetime.fromtimestamp(ns // 1_000_000_000, tz=timezone.utc).strftime("%Y-%m-%dT%H:%M:%S") + f".{ns % 1_000_000_000:09d}Z"


class Delivery:
    """An event from a durable subscription; ack after handling (the iterator acks automatically when you move on)."""

    def __init__(self, event: Event, msg: Any) -> None:
        self.event = event
        self._msg = msg
        self.settled = False

    async def ack(self) -> None:
        if not self.settled:
            self.settled = True
            await self._msg.ack()

    async def nak(self, delay: Optional[float] = None) -> None:
        if not self.settled:
            self.settled = True
            await self._msg.nak(delay=delay)

    async def term(self) -> None:
        if not self.settled:
            self.settled = True
            await self._msg.term()


class Client:
    def __init__(self, nc: NATS, client_id: str, source: str, stream: str) -> None:
        self.nc = nc
        self.js = nc.jetstream()
        self.client_id = client_id
        self.source = source
        self.stream = stream

    async def close(self) -> None:
        await self.nc.drain()

    async def __aenter__(self) -> "Client":
        return self

    async def __aexit__(self, *exc: Any) -> None:
        await self.close()

    # --- publish

    async def publish(self, subject: str, type: str, data: Any = None, *, id: str = "", time: Any = None,
                      content_type: str = "", source: str = "", headers: Optional[Dict[str, str]] = None,
                      blob: Optional[BlobRef] = None) -> PubAck:
        """Stores an event and waits for the ack. data: bytes (octet-stream), str (text), None, or anything JSON."""
        if data is None:
            body, ct = b"", ""
        elif isinstance(data, (bytes, bytearray)):
            body, ct = bytes(data), envelope.CONTENT_BINARY
        elif isinstance(data, str):
            body, ct = data.encode(), envelope.CONTENT_TEXT
        else:
            body, ct = json.dumps(data, separators=(",", ":"), ensure_ascii=False).encode(), envelope.CONTENT_JSON
        e = Event(subject=subject, data=body, id=id, type=type, source=source or self.source, time_ns=envelope.to_ns(time),
                  content_type=content_type or ct, blob=blob, extra={k: [v] for k, v in (headers or {}).items()})
        subj, h, payload = envelope.encode(e)
        ack = await self.js.publish(subj, payload, headers=h)
        return PubAck(id=h[envelope.HEADER_ID], stream=ack.stream, sequence=ack.seq, duplicate=bool(ack.duplicate))

    async def request(self, subject: str, data: bytes = b"", timeout: float = 5.0) -> bytes:
        """A core NATS request (request/reply services); returns the reply payload."""
        msg = await self.nc.request(subject, data, timeout=timeout)
        return msg.data

    # --- subscribe

    async def subscribe(self, filter: str, *, since: Union[str, datetime, int, None] = None, seq: Optional[int] = None,
                        new: bool = False, durable: Optional[str] = None, stream: Optional[str] = None,
                        batch: int = 100, ack_wait: float = 30.0, max_deliver: int = -1) -> AsyncIterator[Any]:
        """Yields events matching filter, in order. Without durable: an ordered, ack-less consumer that resumes after
        reconnects (yields Event). With durable: the consumer "<client-id>_<durable>" keeps its position on the server
        and yields Delivery objects (ack/nak/term). A delivery still unsettled when the loop moves to the next one is
        acked; one unsettled when you leave the loop, and messages fetched but not yet yielded, are redelivered after
        ack_wait (at-least-once, as with the Go SDK's Stop)."""
        stream = stream or self.stream
        if stream == DEFAULT_STREAM:
            envelope.validate_filter(filter)
        policy, opt_time, opt_seq = api.DeliverPolicy.ALL, None, None
        since_ns = parse_since(since)
        if since_ns is not None:
            policy, opt_time = api.DeliverPolicy.BY_START_TIME, _rfc3339(since_ns)
        elif seq is not None:
            policy, opt_seq = api.DeliverPolicy.BY_START_SEQUENCE, seq
        elif new:
            policy = api.DeliverPolicy.NEW
        if durable:
            async for d in self._durable(stream, filter, durable, policy, opt_time, opt_seq, batch, ack_wait, max_deliver):
                yield d
        else:
            async for e in self._ordered(stream, filter, policy, opt_time, opt_seq, batch):
                yield e

    @staticmethod
    def _event(m: Any) -> Event:
        e = envelope.decode(m.subject, m.headers, m.data)
        md = m.metadata
        e.stream, e.sequence = md.stream, md.sequence.stream
        # The ack subject carries the stored time in nanoseconds: $JS.ACK.<stream>.<consumer>.<delivered>.<sseq>.<cseq>.<ns>.<pending>
        # (v1, 9 tokens) or $JS.ACK.<domain>.<account>.<stream>.<consumer>.<delivered>.<sseq>.<cseq>.<ns>.<pending>.<token> (v2).
        toks = (m.reply or "").split(".")
        idx = 7 if len(toks) == 9 else 9 if len(toks) >= 11 else -1
        if idx >= 0 and toks[idx].isdigit():
            e.stream_time_ns = int(toks[idx])
        return e

    async def _ordered(self, stream: str, filter: str, policy: Any, opt_time: Optional[str], opt_seq: Optional[int],
                       batch: int) -> AsyncIterator[Event]:
        last_seq: Optional[int] = None
        n = 0
        while True:
            n += 1
            name = f"pulse_{envelope.new_id().lower()}_{n}"
            cfg = api.ConsumerConfig(name=name, filter_subject=filter, ack_policy=api.AckPolicy.NONE, deliver_policy=policy,
                                     opt_start_time=opt_time, opt_start_seq=opt_seq, inactive_threshold=300.0, mem_storage=True,
                                     num_replicas=1, max_deliver=1)
            if last_seq is not None:  # resume right after the last delivered event
                cfg.deliver_policy, cfg.opt_start_seq, cfg.opt_start_time = api.DeliverPolicy.BY_START_SEQUENCE, last_seq + 1, None
            await self.js.add_consumer(stream, cfg)
            psub = await self.js.pull_subscribe_bind(consumer=name, stream=stream)
            expected = 1
            try:
                while True:
                    try:
                        msgs = await psub.fetch(batch, timeout=5)
                    except NatsTimeout:
                        continue
                    for m in msgs:
                        md = m.metadata
                        if md.sequence.consumer != expected:  # gap: recreate from the last good event
                            raise _Reset()
                        expected += 1
                        last_seq = md.sequence.stream
                        yield self._event(m)
            except (_Reset, NotFoundError):
                pass
            finally:
                await psub.unsubscribe()

    async def _durable(self, stream: str, filter: str, durable: str, policy: Any, opt_time: Optional[str],
                       opt_seq: Optional[int], batch: int, ack_wait: float, max_deliver: int) -> AsyncIterator[Delivery]:
        name = f"{self.client_id}_{durable}"
        cfg = api.ConsumerConfig(name=name, durable_name=name, filter_subject=filter, ack_policy=api.AckPolicy.EXPLICIT,
                                 deliver_policy=policy, opt_start_time=opt_time, opt_start_seq=opt_seq, ack_wait=ack_wait,
                                 max_deliver=max_deliver)
        # An existing durable keeps its server-side position: bind to it (updating it would fail for start changes).
        try:
            await self.js.consumer_info(stream, name)
        except NotFoundError:
            await self.js.add_consumer(stream, cfg)
        psub = await self.js.pull_subscribe_bind(consumer=name, stream=stream)
        try:
            while True:
                try:
                    msgs = await psub.fetch(batch, timeout=5)
                except NatsTimeout:
                    continue
                for m in msgs:
                    d = Delivery(self._event(m), m)
                    yield d
                    if not d.settled:
                        await d.ack()
        finally:
            await psub.unsubscribe()

    # --- blobs (Object Store "blobs", wire-compatible with the Go SDK and jetstream.ObjectStore)

    async def put_blob(self, data: bytes, ext: str = "") -> BlobRef:
        name = envelope.new_id() + ext
        nuid = envelope.new_id()
        chunk_subj = f"$O.{BLOBS_BUCKET}.C.{nuid}"
        digest = hashlib.sha256()
        chunks = 0
        for i in range(0, max(len(data), 1), _CHUNK):
            part = data[i:i + _CHUNK]
            if not part and chunks:
                break
            if part:
                await self.js.publish(chunk_subj, part)
                digest.update(part)
                chunks += 1
        sha = digest.digest()
        info = {"name": name, "options": {"max_chunk_size": _CHUNK}, "bucket": BLOBS_BUCKET, "nuid": nuid, "size": len(data),
                "mtime": datetime.now(timezone.utc).isoformat().replace("+00:00", "Z"), "chunks": chunks,
                "digest": "SHA-256=" + base64.urlsafe_b64encode(sha).decode()}
        meta_subj = f"$O.{BLOBS_BUCKET}.M.{base64.urlsafe_b64encode(name.encode()).decode()}"
        await self.js.publish(meta_subj, json.dumps(info).encode(), headers={"Nats-Rollup": "sub"})
        return BlobRef(bucket=BLOBS_BUCKET, name=name, size=len(data), sha256=sha.hex())

    async def get_blob(self, ref: BlobRef) -> bytes:
        stream = f"OBJ_{ref.bucket}"
        meta_subj = f"$O.{ref.bucket}.M.{base64.urlsafe_b64encode(ref.name.encode()).decode()}"
        resp = await self.nc.request(f"$JS.API.DIRECT.GET.{stream}.{meta_subj}", b"", timeout=5)
        if resp.headers and resp.headers.get("Status") == "404":
            raise NotFoundError()
        info = json.loads(resp.data)
        if info.get("deleted"):
            raise NotFoundError()
        out = bytearray()
        if info.get("chunks", 0):
            name = f"pulse_blob_{envelope.new_id().lower()}"
            await self.js.add_consumer(stream, api.ConsumerConfig(name=name, filter_subject=f"$O.{ref.bucket}.C.{info['nuid']}",
                                                                  ack_policy=api.AckPolicy.NONE, mem_storage=True, num_replicas=1,
                                                                  inactive_threshold=60.0))
            psub = await self.js.pull_subscribe_bind(consumer=name, stream=stream)
            try:
                got = 0
                while got < info["chunks"]:
                    for m in await psub.fetch(min(info["chunks"] - got, 256), timeout=10):
                        out += m.data
                        got += 1
            finally:
                await psub.unsubscribe()
        if ref.sha256 and hashlib.sha256(out).hexdigest() != ref.sha256:
            raise ValueError(f"pulse: blob {ref.name} checksum mismatch")
        return bytes(out)


class _Reset(Exception):
    pass


def tls_context(ca: Optional[str] = None) -> ssl.SSLContext:
    """The system roots plus ca (a PEM file), so a CA added for the internal bootstrap certificate keeps working once
    4222 serves a publicly trusted one."""
    ctx = ssl.create_default_context()
    if ca:
        ctx.load_verify_locations(cafile=ca)
    return ctx


async def connect(url: str, *, creds: Optional[str] = None, creds_data: Optional[str] = None, source: str = "",
                  name: str = "", ca: Optional[str] = None, stream: str = DEFAULT_STREAM, **nats_options: Any) -> Client:
    """Connects with a client's .creds (path or content). The inbox prefix _INBOX_<client-id> comes from the creds."""
    if creds_data is None:
        if creds is None:
            raise ValueError("pulse: creds (path) or creds_data (content) is required")
        with open(creds, encoding="utf-8") as f:
            creds_data = f.read()
    cid = client_id_from_creds(creds_data)
    opts: Dict[str, Any] = dict(servers=[url], user_credentials=RawCredentials(creds_data), inbox_prefix=f"_INBOX_{cid}".encode(),
                                name=name or source or f"pulse-py-{cid}", max_reconnect_attempts=-1, reconnect_time_wait=1)
    if url.startswith("tls://") or ca:
        opts["tls"] = tls_context(ca)
    opts.update(nats_options)
    nc = await nats.connect(**opts)
    return Client(nc, cid, source, stream)
