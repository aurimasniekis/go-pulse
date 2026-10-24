// PL-SDK-004: the TS envelope against sdk/conformance/vectors.json.
import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { decode, encode, formatTime, newId, parseTime, SubjectError, validateFilter, validateSubject, type BlobRef } from '../src/envelope.js'
import { clientIdFromCreds, parseSince } from '../src/client.js'

interface VEvent {
  subject: string
  id?: string
  type?: string
  source?: string
  time?: string
  content_type?: string
  data_b64?: string
  blob?: BlobRef
}
const V = JSON.parse(readFileSync(new URL('../../conformance/vectors.json', import.meta.url), 'utf8')) as {
  subjects: { valid: string[]; invalid: string[] }
  filters: { valid: string[]; invalid: string[] }
  encode: { name: string; event: VEvent; headers: Record<string, string> }[]
  encode_errors: { name: string; event: VEvent }[]
  decode: {
    name: string
    subject: string
    headers: Record<string, string>
    data_b64: string
    event: { subject: string; untyped: boolean; id: string; type: string; source: string; time: string | null; content_type: string; data_b64: string; extra: Record<string, string[]> }
  }[]
}

const b64 = (s = '') => Uint8Array.from(atob(s), (c) => c.charCodeAt(0))
const toB64 = (u: Uint8Array) => btoa(String.fromCharCode(...u))
const ev = (e: VEvent) => ({
  subject: e.subject,
  id: e.id,
  type: e.type,
  source: e.source,
  timeNs: e.time ? parseTime(e.time) : undefined,
  contentType: e.content_type,
  data: b64(e.data_b64),
  blob: e.blob,
})

describe('subjects and filters', () => {
  it.each(V.subjects.valid)('valid subject %s', (s) => validateSubject(s))
  it.each(V.subjects.invalid)('invalid subject %s', (s) => expect(() => validateSubject(s)).toThrow(SubjectError))
  it.each(V.filters.valid)('valid filter %s', (f) => validateFilter(f))
  it.each(V.filters.invalid)('invalid filter %s', (f) => expect(() => validateFilter(f)).toThrow(SubjectError))
})

describe('encode', () => {
  it.each(V.encode.map((v) => [v.name, v] as const))('%s', (_, v) => {
    const out = encode(ev(v.event))
    expect(out.headers).toEqual(v.headers)
    expect(toB64(out.data)).toBe(v.event.data_b64 ?? '')
  })
  it.each(V.encode_errors.map((v) => [v.name, v] as const))('error: %s', (_, v) => expect(() => encode(ev(v.event))).toThrow(SubjectError))
})

describe('decode', () => {
  it.each(V.decode.map((v) => [v.name, v] as const))('%s', (_, v) => {
    const e = decode(v.subject, v.headers, b64(v.data_b64))
    const w = v.event
    expect([e.subject, e.untyped, e.id, e.type, e.source, e.contentType]).toEqual([w.subject, w.untyped, w.id, w.type, w.source, w.content_type])
    expect(e.time ?? null).toBe(w.time)
    expect(toB64(e.data)).toBe(w.data_b64)
    expect(e.extra).toEqual(w.extra)
  })
})

it('ids are ULIDs and times keep nanoseconds', () => {
  const ids = new Set(Array.from({ length: 1000 }, newId))
  expect(ids.size).toBe(1000)
  for (const i of ids) expect(i).toMatch(/^[0-7][0-9A-HJKMNP-TV-Z]{25}$/)
  expect(formatTime(parseTime('2026-10-23T10:11:12.123456789Z')!)).toBe('2026-10-23T10:11:12.123456789Z')
  expect(parseTime('2026-10-23T12:11:12.5+02:00')).toBe(parseTime('2026-10-23T10:11:12.5Z'))
  expect(parseTime('2026-02-30T00:00:00Z')).toBeUndefined()
  expect(parseTime('nope')).toBeUndefined()
})

it('since durations and client ids from creds', () => {
  const now = BigInt(Date.now()) * 1_000_000n
  const d = now - parseSince('1h30m')!
  expect(d > 5_399_000_000_000n && d < 5_401_000_000_000n).toBe(true)
  const payload = btoa(JSON.stringify({ nats: { tags: ['pulse-client:c_abc123'] } })).replace(/=+$/, '')
  const creds = `-----BEGIN NATS USER JWT-----\neyJ0eXAiOiJKV1QifQ.${payload}.sig\n------END NATS USER JWT------\n`
  expect(clientIdFromCreds(creds)).toBe('c_abc123')
})
