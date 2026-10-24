// Pulse client on @nats-io v3 (PL-SDK-003), for node (tls:// or nats://) and browsers (ws:// or wss:// through /ws). Same
// surface as the Go SDK, within a scoped client's grants: consumers are created by name with a single filter subject.
import { credsAuthenticator, headers as natsHeaders, wsconnect, type ConnectionOptions, type MsgHdrs, type NatsConnection } from '@nats-io/nats-core'
import {
  AckPolicy,
  DeliverPolicy,
  jetstream,
  jetstreamManager,
  type ConsumerMessages,
  type JetStreamClient,
  type JsMsg,
} from '@nats-io/jetstream'
import { withRoots } from './tls.js'
import { ContentBinary, ContentJSON, ContentText, decode, encode, Event, newId, nowNs, toNs, validateFilter, type BlobRef, type TimeInput } from './envelope.js'

export const DefaultStream = 'EVENTS'
export const BlobsBucket = 'blobs'
const clientTag = 'pulse-client:'
const chunkSize = 128 * 1024

function b64urlDecode(s: string): Uint8Array {
  const b = atob(s.replace(/-/g, '+').replace(/_/g, '/') + '='.repeat((4 - (s.length % 4)) % 4))
  return Uint8Array.from(b, (c) => c.charCodeAt(0))
}

function b64url(bytes: Uint8Array): string {
  let s = ''
  for (const b of bytes) s += String.fromCharCode(b)
  return btoa(s).replace(/\+/g, '-').replace(/\//g, '_') // padded, as Go's base64.URLEncoding
}

function hex(bytes: Uint8Array): string {
  return Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('')
}

/** The Pulse client ID from the user JWT tags in .creds content (the inbox prefix, PL-SDK-007). */
export function clientIdFromCreds(creds: string): string {
  const m = /-----BEGIN NATS USER JWT-----\s*([A-Za-z0-9_\-.=]+)\s*------END NATS USER JWT------/.exec(creds)
  if (!m) throw new Error('pulse: creds contain no user JWT')
  const claims = JSON.parse(new TextDecoder().decode(b64urlDecode(m[1]!.split('.')[1]!))) as { nats?: { tags?: string[] } }
  for (const t of claims.nats?.tags ?? []) if (t.startsWith(clientTag)) return t.slice(clientTag.length)
  throw new Error('pulse: creds carry no pulse client tag')
}

export interface ConnectOptions {
  servers: string | string[]
  /** .creds content (read the file yourself in node; browsers get it from the operator). */
  creds: string | Uint8Array
  source?: string
  name?: string
  stream?: string
  /** Extra CA (PEM) for tls:// in node, trusted next to node's root certificates (e.g. the internal CA while 4222 serves
   * the bootstrap certificate). */
  ca?: string
  nats?: Partial<ConnectionOptions>
}

export interface PublishOptions {
  id?: string
  time?: TimeInput
  contentType?: string
  source?: string
  headers?: Record<string, string>
  blob?: BlobRef
}

export interface PubAck {
  id: string
  stream: string
  sequence: number
  duplicate: boolean
}

export interface SubscribeOptions {
  /** '24h', '7d', '1h30m', an RFC 3339 string, a Date or bigint nanoseconds. */
  since?: string | Date | bigint
  seq?: number
  new?: boolean
  /** Durable consumer "<client-id>_<durable>" (needs a durable grant); events carry ack/nak/term. */
  durable?: string
  stream?: string
  ackWaitMs?: number
}

/** An event from a durable subscription. Unsettled deliveries are acked when the loop moves on. */
export class Delivery extends Event {
  settled = false
  #msg: JsMsg | undefined

  static from(e: Event, m: JsMsg): Delivery {
    const d = Object.assign(new Delivery(), e)
    d.#msg = m
    return d
  }

  ack(): void {
    if (!this.settled) {
      this.settled = true
      this.#msg?.ack()
    }
  }

  nak(delayMs?: number): void {
    if (!this.settled) {
      this.settled = true
      this.#msg?.nak(delayMs)
    }
  }

  term(): void {
    if (!this.settled) {
      this.settled = true
      this.#msg?.term()
    }
  }
}

const durRe = /(\d+)(ms|s|m|h|d|w)/g
const unitMs: Record<string, number> = { ms: 1, s: 1000, m: 60_000, h: 3_600_000, d: 86_400_000, w: 604_800_000 }

export function parseSince(since: SubscribeOptions['since']): bigint | undefined {
  if (since === undefined) return undefined
  if (typeof since === 'string' && since.trim() && since.trim().replace(durRe, '') === '') {
    let ms = 0
    for (const m of since.trim().matchAll(durRe)) ms += Number(m[1]) * unitMs[m[2]!]!
    return nowNs() - BigInt(ms) * 1_000_000n
  }
  return toNs(since)
}

function hdrsToRecord(h: MsgHdrs | undefined): Record<string, string[]> {
  const out: Record<string, string[]> = {}
  if (!h) return out
  for (const k of h.keys()) out[k] = h.values(k)
  return out
}

function toEvent(m: JsMsg): Event {
  const e = decode(m.subject, hdrsToRecord(m.headers), m.data)
  e.stream = m.info.stream
  e.sequence = m.info.streamSequence
  e.streamTimeNs = m.timestampNanos
  return e
}

function rfc3339(ns: bigint): string {
  return new Date(Number(ns / 1_000_000n)).toISOString().slice(0, 19) + '.' + (ns % 1_000_000_000n).toString().padStart(9, '0') + 'Z'
}

export class Client {
  readonly js: JetStreamClient

  constructor(
    readonly nc: NatsConnection,
    readonly clientId: string,
    readonly source: string,
    readonly stream: string,
  ) {
    this.js = jetstream(nc)
  }

  async close(): Promise<void> {
    await this.nc.drain()
  }

  /** Stores an event and waits for the ack. data: Uint8Array (octet-stream), string (text), undefined, or JSON. */
  async publish(subject: string, type: string, data?: unknown, o: PublishOptions = {}): Promise<PubAck> {
    let body: Uint8Array = new Uint8Array()
    let ct = ''
    if (data instanceof Uint8Array) [body, ct] = [data, ContentBinary]
    else if (typeof data === 'string') [body, ct] = [new TextEncoder().encode(data), ContentText]
    else if (data !== undefined && data !== null) [body, ct] = [new TextEncoder().encode(JSON.stringify(data)), ContentJSON]
    const extra: Record<string, string[]> = {}
    for (const [k, v] of Object.entries(o.headers ?? {})) extra[k] = [v]
    const enc = encode({ subject, type, data: body, id: o.id, source: o.source || this.source, timeNs: toNs(o.time), contentType: o.contentType || ct, blob: o.blob, extra })
    const h = natsHeaders()
    for (const [k, v] of Object.entries(enc.headers)) h.set(k, v)
    const ack = await this.js.publish(enc.subject, enc.data, { headers: h })
    return { id: enc.headers['Nats-Msg-Id']!, stream: ack.stream, sequence: ack.seq, duplicate: ack.duplicate }
  }

  /** A core NATS request (request/reply services); resolves to the reply payload. */
  async request(subject: string, data: Uint8Array | string = new Uint8Array(), timeoutMs = 5000): Promise<Uint8Array> {
    const m = await this.nc.request(subject, data, { timeout: timeoutMs })
    return m.data
  }

  /** Events matching filter, in order: an ordered consumer (resumes after reconnects), or a durable one. */
  async *subscribe(filter: string, o: SubscribeOptions = {}): AsyncGenerator<Event | Delivery> {
    const stream = o.stream ?? this.stream
    if (stream === DefaultStream) validateFilter(filter)
    let deliver_policy: DeliverPolicy = DeliverPolicy.All
    let opt_start_time: string | undefined
    let opt_start_seq: number | undefined
    const since = parseSince(o.since)
    if (since !== undefined) [deliver_policy, opt_start_time] = [DeliverPolicy.StartTime, rfc3339(since)]
    else if (o.seq !== undefined) [deliver_policy, opt_start_seq] = [DeliverPolicy.StartSequence, o.seq]
    else if (o.new) deliver_policy = DeliverPolicy.New

    let msgs: ConsumerMessages
    if (o.durable) {
      const name = `${this.clientId}_${o.durable}`
      const jsm = await jetstreamManager(this.nc)
      try {
        await jsm.consumers.add(stream, {
          name,
          durable_name: name,
          filter_subject: filter,
          ack_policy: AckPolicy.Explicit,
          deliver_policy,
          ...(opt_start_time ? { opt_start_time } : {}),
          ...(opt_start_seq !== undefined ? { opt_start_seq } : {}),
          ...(o.ackWaitMs ? { ack_wait: o.ackWaitMs * 1_000_000 } : {}),
        })
      } catch (err) {
        if (!/already (exists|in use)/i.test(String(err))) throw err // keep the server's position
      }
      msgs = await (await this.js.consumers.get(stream, name)).consume()
      try {
        for await (const m of msgs) {
          const d = Delivery.from(toEvent(m), m)
          yield d
          if (!d.settled) d.ack()
        }
      } finally {
        msgs.stop() // otherwise drain() waits for the open pull to expire
      }
      return
    }
    const c = await this.js.consumers.get(stream, {
      filter_subjects: filter, // a string keeps the filtered create subject the grants allow
      deliver_policy,
      ...(opt_start_time ? { opt_start_time } : {}),
      ...(opt_start_seq !== undefined ? { opt_start_seq } : {}),
    })
    msgs = await c.consume()
    try {
      for await (const m of msgs) yield toEvent(m)
    } finally {
      msgs.stop()
    }
  }

  /** Stores data in the blobs Object Store (wire-compatible with the Go SDK) and returns a reference for publish. */
  async putBlob(data: Uint8Array, ext = ''): Promise<BlobRef> {
    const name = newId() + ext
    const nuid = newId()
    for (let i = 0; i < data.length; i += chunkSize) await this.js.publish(`$O.${BlobsBucket}.C.${nuid}`, data.subarray(i, i + chunkSize))
    const sha = new Uint8Array(await globalThis.crypto.subtle.digest('SHA-256', data as Uint8Array<ArrayBuffer>))
    const info = {
      name,
      options: { max_chunk_size: chunkSize },
      bucket: BlobsBucket,
      nuid,
      size: data.length,
      mtime: new Date().toISOString(),
      chunks: Math.ceil(data.length / chunkSize),
      digest: 'SHA-256=' + b64url(sha),
    }
    const h = natsHeaders()
    h.set('Nats-Rollup', 'sub')
    await this.js.publish(`$O.${BlobsBucket}.M.${b64url(new TextEncoder().encode(name))}`, JSON.stringify(info), { headers: h })
    return { bucket: BlobsBucket, name, size: data.length, sha256: hex(sha) }
  }

  /** Reads a blob and verifies its SHA-256 when the reference carries one. */
  async getBlob(ref: BlobRef): Promise<Uint8Array> {
    const stream = `OBJ_${ref.bucket}`
    const meta = `$O.${ref.bucket}.M.${b64url(new TextEncoder().encode(ref.name))}`
    const r = await this.nc.request(`$JS.API.DIRECT.GET.${stream}.${meta}`, new Uint8Array(), { timeout: 5000 })
    if (r.headers?.code === 404 || r.data.length === 0) throw new Error(`pulse: blob ${ref.name} not found`)
    const info = JSON.parse(new TextDecoder().decode(r.data)) as { nuid: string; chunks: number; size: number; deleted?: boolean }
    if (info.deleted) throw new Error(`pulse: blob ${ref.name} was deleted`)
    const out = new Uint8Array(info.size)
    let off = 0
    if (info.chunks > 0) {
      const c = await this.js.consumers.get(stream, { filter_subjects: `$O.${ref.bucket}.C.${info.nuid}` })
      const msgs = await c.consume()
      let n = 0
      for await (const m of msgs) {
        out.set(m.data, off)
        off += m.data.length
        if (++n === info.chunks) break
      }
      msgs.stop()
    }
    if (ref.sha256) {
      const sum = hex(new Uint8Array(await globalThis.crypto.subtle.digest('SHA-256', out)))
      if (sum !== ref.sha256) throw new Error(`pulse: blob ${ref.name} checksum mismatch`)
    }
    return out
  }
}

/** Connects with a client's .creds. ws:// and wss:// use the WebSocket transport (browsers, or node ≥ 22 via /ws). */
export async function connect(o: ConnectOptions): Promise<Client> {
  const credsText = typeof o.creds === 'string' ? o.creds : new TextDecoder().decode(o.creds)
  const id = clientIdFromCreds(credsText)
  const servers = Array.isArray(o.servers) ? o.servers : [o.servers]
  const opts: ConnectionOptions = {
    servers,
    authenticator: credsAuthenticator(new TextEncoder().encode(credsText)),
    inboxPrefix: `_INBOX_${id}`,
    name: o.name || o.source || `pulse-ts-${id}`,
    maxReconnectAttempts: -1,
    reconnectTimeWait: 1000,
    ...o.nats,
  }
  let nc: NatsConnection
  if (servers.every((s) => /^wss?:\/\//.test(s))) nc = await wsconnect(opts)
  else {
    const mod = '@nats-io/transport-node' // kept out of browser bundles
    const { connect: tcp } = (await import(/* @vite-ignore */ mod)) as { connect: (o: ConnectionOptions) => Promise<NatsConnection> }
    if (o.ca && !o.nats?.tls) {
      const tlsMod = 'node:tls'
      const { rootCertificates } = (await import(/* @vite-ignore */ tlsMod)) as { rootCertificates: readonly string[] }
      opts.tls = { ca: withRoots(o.ca, rootCertificates) }
    }
    nc = await tcp(opts)
  }
  return new Client(nc, id, o.source ?? '', o.stream ?? DefaultStream)
}
