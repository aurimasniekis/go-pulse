// The Pulse event envelope (docs/ENVELOPE.md): CloudEvents-lite in NATS headers, shared with the Go and Python SDKs and
// checked against sdk/conformance/vectors.json (PL-ENV-002, PL-SDK-004). Times are bigint unix nanoseconds: Date stops at
// milliseconds and ce-time carries nanoseconds.

export const HeaderID = 'Nats-Msg-Id'
export const HeaderType = 'ce-type'
export const HeaderSource = 'ce-source'
export const HeaderTime = 'ce-time'
export const HeaderContentType = 'content-type'
export const HeaderBlob = 'pulse-blob'
export const HeaderBlobSize = 'pulse-blob-size'
export const HeaderBlobSHA256 = 'pulse-blob-sha256'

export const ContentJSON = 'application/json'
export const ContentText = 'text/plain; charset=utf-8'
export const ContentBinary = 'application/octet-stream'

export const SubjectPrefix = 'evt.'
export const MinTokens = 4
export const MaxSubjectLen = 256

const envelopeHeaders = new Set([HeaderID, HeaderType, HeaderSource, HeaderTime, HeaderContentType, HeaderBlob, HeaderBlobSize, HeaderBlobSHA256])
const token = /^[a-z0-9_-]+$/

export class SubjectError extends Error {
  override name = 'SubjectError'
}

export function validateSubject(s: string): void {
  if (new TextEncoder().encode(s).length > MaxSubjectLen) throw new SubjectError(`invalid event subject: longer than ${MaxSubjectLen} bytes`)
  if (!s.startsWith(SubjectPrefix)) throw new SubjectError(`invalid event subject "${s}": must start with "${SubjectPrefix}"`)
  const toks = s.split('.')
  if (toks.length < MinTokens) throw new SubjectError(`invalid event subject "${s}": need evt.<domain>.<entity>.<action>`)
  for (const t of toks) if (!token.test(t)) throw new SubjectError(`invalid event subject "${s}": token "${t}" must match [a-z0-9_-]+`)
}

export function validateFilter(f: string): void {
  if (f === 'evt.>') return
  if (!f.startsWith(SubjectPrefix)) throw new SubjectError(`invalid filter "${f}": must start with "${SubjectPrefix}"`)
  const toks = f.split('.')
  toks.forEach((t, i) => {
    if ((t === '>' && i === toks.length - 1) || t === '*' || token.test(t)) return
    throw new SubjectError(`invalid filter "${f}": bad token "${t}"`)
  })
}

const rfc3339 = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})$/

/** RFC 3339 (as Go's time.RFC3339Nano parses it) → unix nanoseconds; undefined when malformed. */
export function parseTime(s: string | undefined | null): bigint | undefined {
  const m = rfc3339.exec(s ?? '')
  if (!m) return undefined
  const [, y, mo, d, h, mi, sec, frac, off] = m
  const ms = Date.UTC(+y!, +mo! - 1, +d!, +h!, +mi!, +sec!)
  const dt = new Date(ms)
  if (Number.isNaN(ms) || dt.getUTCDate() !== +d! || dt.getUTCMonth() !== +mo! - 1 || +h! > 23 || +mi! > 59 || +sec! > 59) return undefined
  let ns = BigInt(ms / 1000) * 1_000_000_000n + (frac ? BigInt(frac.padEnd(9, '0')) : 0n)
  if (off !== 'Z') {
    const sign = off![0] === '+' ? 1n : -1n
    ns -= sign * (BigInt(+off!.slice(1, 3)) * 3600n + BigInt(+off!.slice(4, 6)) * 60n) * 1_000_000_000n
  }
  return ns
}

/** Unix nanoseconds → RFC 3339 in UTC with trailing zeros trimmed (Go's time.RFC3339Nano). */
export function formatTime(ns: bigint): string {
  const sec = ns / 1_000_000_000n - (ns % 1_000_000_000n < 0n ? 1n : 0n)
  const frac = ns - sec * 1_000_000_000n
  let s = new Date(Number(sec) * 1000).toISOString().slice(0, 19)
  if (frac) s += '.' + frac.toString().padStart(9, '0').replace(/0+$/, '')
  return s + 'Z'
}

export type TimeInput = bigint | Date | string

export function toNs(t: TimeInput | undefined): bigint | undefined {
  if (t === undefined) return undefined
  if (typeof t === 'bigint') return t
  if (t instanceof Date) return BigInt(t.getTime()) * 1_000_000n
  const ns = parseTime(t)
  if (ns === undefined) throw new RangeError(`invalid RFC 3339 time "${t}"`)
  return ns
}

export function nowNs(): bigint {
  const hr = (globalThis as { performance?: { timeOrigin: number; now(): number } }).performance
  return hr ? BigInt(Math.round((hr.timeOrigin + hr.now()) * 1e6)) : BigInt(Date.now()) * 1_000_000n
}

const crockford = '0123456789ABCDEFGHJKMNPQRSTVWXYZ'

/** A new ULID (Crockford base32), as the Go SDK uses for Nats-Msg-Id. */
export function newId(): string {
  const rnd = new Uint8Array(10)
  globalThis.crypto.getRandomValues(rnd)
  let v = BigInt(Date.now()) << 80n
  for (const [i, b] of rnd.entries()) v |= BigInt(b) << BigInt(8 * (9 - i))
  let out = ''
  for (let i = 25; i >= 0; i--) out += crockford[Number((v >> BigInt(5 * i)) & 31n)]
  return out
}

/** Points at an Object Store object holding a payload larger than max_payload (PL-ENV-004). */
export interface BlobRef {
  bucket: string
  name: string
  size: number
  sha256: string
}

export class Event {
  subject = ''
  id = ''
  type = '' // empty: untyped (e.g. a raw `nats pub`)
  source = ''
  timeNs: bigint | undefined = undefined
  contentType = ''
  data: Uint8Array = new Uint8Array()
  blob: BlobRef | undefined = undefined
  extra: Record<string, string[]> = {}
  // read side
  stream = ''
  sequence = 0
  streamTimeNs: bigint | undefined = undefined

  constructor(init?: Partial<Event>) {
    Object.assign(this, init)
  }

  get untyped(): boolean {
    return this.type === ''
  }

  /** ce-time as RFC 3339 (nanoseconds kept), or undefined. */
  get time(): string | undefined {
    return this.timeNs === undefined ? undefined : formatTime(this.timeNs)
  }

  text(): string {
    return new TextDecoder().decode(this.data)
  }

  json<T = unknown>(): T {
    return JSON.parse(this.text()) as T
  }
}

export interface Encoded {
  subject: string
  headers: Record<string, string>
  data: Uint8Array
}

/** Headers and payload for publishing; fills id and time when empty and validates the subject. */
export function encode(e: Partial<Event> & { subject: string }): Encoded {
  validateSubject(e.subject)
  const h: Record<string, string> = {}
  for (const [k, vs] of Object.entries(e.extra ?? {})) if (vs.length) h[k] = vs[vs.length - 1]!
  h[HeaderID] = e.id || newId()
  if (e.type) h[HeaderType] = e.type
  if (e.source) h[HeaderSource] = e.source
  h[HeaderTime] = formatTime(e.timeNs ?? nowNs())
  if (e.contentType) h[HeaderContentType] = e.contentType
  if (e.blob) {
    h[HeaderBlob] = `${e.blob.bucket}/${e.blob.name}`
    h[HeaderBlobSize] = String(e.blob.size)
    if (e.blob.sha256) h[HeaderBlobSHA256] = e.blob.sha256
  }
  return { subject: e.subject, headers: h, data: e.data ?? new Uint8Array() }
}

/** Never fails: missing or malformed fields stay empty (untyped events are valid, PL-BUS-003). */
export function decode(subject: string, headers: Record<string, string[] | string> | undefined, data: Uint8Array): Event {
  const h: Record<string, string[]> = {}
  for (const [k, v] of Object.entries(headers ?? {})) h[k] = Array.isArray(v) ? v : [v]
  const get = (k: string) => h[k]?.[0] ?? ''
  const e = new Event({ subject, data, id: get(HeaderID), type: get(HeaderType), source: get(HeaderSource), contentType: get(HeaderContentType) })
  e.timeNs = parseTime(get(HeaderTime))
  const ref = get(HeaderBlob)
  const slash = ref.indexOf('/')
  if (slash > 0) {
    const size = Number.parseInt(get(HeaderBlobSize) || '0', 10)
    e.blob = { bucket: ref.slice(0, slash), name: ref.slice(slash + 1), size: Number.isNaN(size) ? 0 : size, sha256: get(HeaderBlobSHA256) }
  }
  for (const [k, vs] of Object.entries(h)) {
    if (envelopeHeaders.has(k) || k.startsWith('Nats-')) continue
    e.extra[k] = vs
  }
  return e
}
