"""The Pulse event envelope (docs/ENVELOPE.md): CloudEvents-lite in NATS headers, shared with the Go and TS SDKs and
checked against sdk/conformance/vectors.json (PL-ENV-002, PL-SDK-004)."""

from __future__ import annotations

import json
import os
import re
import time as _time
from dataclasses import dataclass, field
from datetime import datetime, timezone
from typing import Any, Dict, List, Mapping, Optional

HEADER_ID = "Nats-Msg-Id"
HEADER_TYPE = "ce-type"
HEADER_SOURCE = "ce-source"
HEADER_TIME = "ce-time"
HEADER_CONTENT_TYPE = "content-type"
HEADER_BLOB = "pulse-blob"
HEADER_BLOB_SIZE = "pulse-blob-size"
HEADER_BLOB_SHA256 = "pulse-blob-sha256"

CONTENT_JSON = "application/json"
CONTENT_TEXT = "text/plain; charset=utf-8"
CONTENT_BINARY = "application/octet-stream"

SUBJECT_PREFIX = "evt."
MIN_TOKENS = 4
MAX_SUBJECT_LEN = 256

_ENVELOPE_HEADERS = {HEADER_ID, HEADER_TYPE, HEADER_SOURCE, HEADER_TIME, HEADER_CONTENT_TYPE, HEADER_BLOB, HEADER_BLOB_SIZE,
                     HEADER_BLOB_SHA256}
_TOKEN = re.compile(r"^[a-z0-9_-]+$")


class SubjectError(ValueError):
    """A subject or filter breaks the grammar evt.<domain>.<entity>.<action>[.<key>…] (PL-BUS-002)."""


def validate_subject(s: str) -> None:
    if len(s.encode()) > MAX_SUBJECT_LEN:
        raise SubjectError(f"invalid event subject: longer than {MAX_SUBJECT_LEN} bytes")
    if not s.startswith(SUBJECT_PREFIX):
        raise SubjectError(f"invalid event subject {s!r}: must start with {SUBJECT_PREFIX!r}")
    toks = s.split(".")
    if len(toks) < MIN_TOKENS:
        raise SubjectError(f"invalid event subject {s!r}: need evt.<domain>.<entity>.<action>")
    for t in toks:
        if not _TOKEN.match(t):
            raise SubjectError(f"invalid event subject {s!r}: token {t!r} must match [a-z0-9_-]+")


def validate_filter(f: str) -> None:
    if f == "evt.>":
        return
    if not f.startswith(SUBJECT_PREFIX):
        raise SubjectError(f"invalid filter {f!r}: must start with {SUBJECT_PREFIX!r}")
    toks = f.split(".")
    for i, t in enumerate(toks):
        if (t == ">" and i == len(toks) - 1) or t == "*" or _TOKEN.match(t):
            continue
        raise SubjectError(f"invalid filter {f!r}: bad token {t!r}")


# --- time: RFC 3339 with up to nanoseconds; Python datetimes stop at microseconds, so times travel as unix nanoseconds.

_RFC3339 = re.compile(r"^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})$")


def parse_time(s: str) -> Optional[int]:
    """Parses RFC 3339 (as Go's time.RFC3339Nano does) into unix nanoseconds; None when malformed."""
    m = _RFC3339.match(s or "")
    if not m:
        return None
    y, mo, d, h, mi, sec, frac, off = m.groups()
    try:
        base = datetime(int(y), int(mo), int(d), int(h), int(mi), int(sec), tzinfo=timezone.utc)
    except ValueError:
        return None
    ns = int(base.timestamp()) * 1_000_000_000 + (int((frac or "").ljust(9, "0")) if frac else 0)
    if off != "Z":
        sign = 1 if off[0] == "+" else -1
        ns -= sign * (int(off[1:3]) * 3600 + int(off[4:6]) * 60) * 1_000_000_000
    return ns


def format_time(ns: int) -> str:
    """Unix nanoseconds → RFC 3339 in UTC with trailing zeros trimmed (Go's time.RFC3339Nano)."""
    sec, frac = divmod(ns, 1_000_000_000)
    s = datetime.fromtimestamp(sec, tz=timezone.utc).strftime("%Y-%m-%dT%H:%M:%S")
    if frac:
        s += "." + f"{frac:09d}".rstrip("0")
    return s + "Z"


def to_ns(t: Any) -> Optional[int]:
    """Accepts unix nanoseconds, a datetime (naive = UTC) or an RFC 3339 string."""
    if t is None:
        return None
    if isinstance(t, bool):
        raise TypeError("time must be int nanoseconds, datetime or RFC 3339 string")
    if isinstance(t, int):
        return t
    if isinstance(t, datetime):
        if t.tzinfo is None:
            t = t.replace(tzinfo=timezone.utc)
        return int(t.timestamp()) * 1_000_000_000 + t.microsecond * 1000
    if isinstance(t, str):
        ns = parse_time(t)
        if ns is None:
            raise ValueError(f"invalid RFC 3339 time {t!r}")
        return ns
    raise TypeError("time must be int nanoseconds, datetime or RFC 3339 string")


# --- ids: ULID (Crockford base32), as the Go SDK uses for Nats-Msg-Id.

_CROCKFORD = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"


def new_id() -> str:
    v = (int(_time.time() * 1000) << 80) | int.from_bytes(os.urandom(10), "big")
    return "".join(_CROCKFORD[(v >> (5 * i)) & 31] for i in range(25, -1, -1))


@dataclass
class BlobRef:
    """Points at an Object Store object holding a payload larger than max_payload (PL-ENV-004)."""

    bucket: str
    name: str
    size: int = 0
    sha256: str = ""


@dataclass
class Event:
    subject: str
    data: bytes = b""
    id: str = ""
    type: str = ""  # empty: untyped (e.g. a raw `nats pub`)
    source: str = ""
    time_ns: Optional[int] = None
    content_type: str = ""
    blob: Optional[BlobRef] = None
    extra: Dict[str, List[str]] = field(default_factory=dict)
    # read side
    stream: str = ""
    sequence: int = 0
    stream_time_ns: Optional[int] = None

    @property
    def untyped(self) -> bool:
        return self.type == ""

    @property
    def time(self) -> Optional[datetime]:
        """ce-time as an aware UTC datetime (microseconds; time_ns keeps nanoseconds)."""
        return None if self.time_ns is None else datetime.fromtimestamp(self.time_ns / 1e9, tz=timezone.utc)

    def json(self) -> Any:
        return json.loads(self.data) if self.data else None

    def text(self) -> str:
        return self.data.decode("utf-8")


def encode(e: Event) -> tuple[str, Dict[str, str], bytes]:
    """Returns (subject, headers, data) for publishing; fills id and time when empty, validates the subject."""
    validate_subject(e.subject)
    h: Dict[str, str] = {}
    for k, vs in (e.extra or {}).items():
        if vs:
            h[k] = vs[-1]
    h[HEADER_ID] = e.id or new_id()
    if e.type:
        h[HEADER_TYPE] = e.type
    if e.source:
        h[HEADER_SOURCE] = e.source
    h[HEADER_TIME] = format_time(e.time_ns if e.time_ns is not None else _time.time_ns())
    if e.content_type:
        h[HEADER_CONTENT_TYPE] = e.content_type
    if e.blob is not None:
        h[HEADER_BLOB] = f"{e.blob.bucket}/{e.blob.name}"
        h[HEADER_BLOB_SIZE] = str(e.blob.size)
        if e.blob.sha256:
            h[HEADER_BLOB_SHA256] = e.blob.sha256
    return e.subject, h, e.data or b""


def decode(subject: str, headers: Optional[Mapping[str, Any]], data: bytes) -> Event:
    """Never fails: missing or malformed fields stay empty (untyped events are valid, PL-BUS-003)."""
    h: Dict[str, List[str]] = {}
    for k, v in (headers or {}).items():
        h[k] = list(v) if isinstance(v, (list, tuple)) else [v]

    def get(k: str) -> str:
        vs = h.get(k)
        return vs[0] if vs else ""

    e = Event(subject=subject, data=data or b"", id=get(HEADER_ID), type=get(HEADER_TYPE), source=get(HEADER_SOURCE),
              content_type=get(HEADER_CONTENT_TYPE), time_ns=parse_time(get(HEADER_TIME)))
    ref = get(HEADER_BLOB)
    if "/" in ref:
        bucket, name = ref.split("/", 1)
        try:
            size = int(get(HEADER_BLOB_SIZE) or 0)
        except ValueError:
            size = 0
        e.blob = BlobRef(bucket=bucket, name=name, size=size, sha256=get(HEADER_BLOB_SHA256))
    for k, vs in h.items():
        if k in _ENVELOPE_HEADERS or k.startswith("Nats-"):
            continue
        e.extra[k] = vs
    return e
