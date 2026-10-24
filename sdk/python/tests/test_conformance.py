"""PL-SDK-004: the Python envelope against sdk/conformance/vectors.json."""

import base64
import json
import pathlib

import pytest

from pulse import BlobRef, Event, SubjectError, decode, encode, format_time, new_id, parse_time, validate_filter, validate_subject

V = json.loads((pathlib.Path(__file__).parents[2] / "conformance" / "vectors.json").read_text())


@pytest.mark.parametrize("s", V["subjects"]["valid"])
def test_valid_subjects(s):
    validate_subject(s)


@pytest.mark.parametrize("s", V["subjects"]["invalid"])
def test_invalid_subjects(s):
    with pytest.raises(SubjectError):
        validate_subject(s)


@pytest.mark.parametrize("f", V["filters"]["valid"])
def test_valid_filters(f):
    validate_filter(f)


@pytest.mark.parametrize("f", V["filters"]["invalid"])
def test_invalid_filters(f):
    with pytest.raises(SubjectError):
        validate_filter(f)


def _event(d):
    b = d.get("blob")
    return Event(subject=d["subject"], id=d.get("id", ""), type=d.get("type", ""), source=d.get("source", ""),
                 time_ns=parse_time(d["time"]) if d.get("time") else None, content_type=d.get("content_type", ""),
                 data=base64.b64decode(d.get("data_b64", "")), blob=BlobRef(**b) if b else None)


@pytest.mark.parametrize("v", V["encode"], ids=[v["name"] for v in V["encode"]])
def test_encode(v):
    _, headers, data = encode(_event(v["event"]))
    assert headers == v["headers"]
    assert data == base64.b64decode(v["event"].get("data_b64", ""))


@pytest.mark.parametrize("v", V["encode_errors"], ids=[v["name"] for v in V["encode_errors"]])
def test_encode_errors(v):
    with pytest.raises(SubjectError):
        encode(_event(v["event"]))


@pytest.mark.parametrize("v", V["decode"], ids=[v["name"] for v in V["decode"]])
def test_decode(v):
    e = decode(v["subject"], v["headers"], base64.b64decode(v["data_b64"]))
    want = v["event"]
    assert e.subject == want["subject"]
    assert e.untyped == want["untyped"]
    assert (e.id, e.type, e.source, e.content_type) == (want["id"], want["type"], want["source"], want["content_type"])
    assert (format_time(e.time_ns) if e.time_ns is not None else None) == want["time"]
    assert base64.b64encode(e.data).decode() == want["data_b64"]
    assert e.extra == want["extra"]


def test_ids_are_ulids():
    ids = {new_id() for _ in range(1000)}
    assert len(ids) == 1000
    assert all(len(i) == 26 and i[0] in "01234567" for i in ids)


def test_time_round_trip_keeps_nanoseconds():
    assert format_time(parse_time("2026-10-23T10:11:12.123456789Z")) == "2026-10-23T10:11:12.123456789Z"
    assert parse_time("2026-10-23T12:11:12.5+02:00") == parse_time("2026-10-23T10:11:12.5Z")
    assert parse_time("not a time") is None
