// PL-SDK-005 interop for node (tls:// and ws://): replays what Go published and publishes for Go to read. Driven by
// internal/sdktest (make test-sdk-interop); prints one JSON document on stdout.
import { readFileSync } from 'node:fs'
import { parseArgs } from 'node:util'
import { connect } from '../dist/index.js'

const { values: a } = parseArgs({ options: { url: { type: 'string' }, creds: { type: 'string' }, ca: { type: 'string' }, since: { type: 'string' }, count: { type: 'string' }, durable: { type: 'string' } } })
const toB64 = (u) => Buffer.from(u).toString('base64')
const evJSON = (e) => ({
  seq: e.sequence, subject: e.subject, id: e.id, type: e.type, source: e.source, time: e.time ?? null, content_type: e.contentType,
  data_b64: toB64(e.data), extra: e.extra, blob: e.blob ?? null, stream_time_ns: String(e.streamTimeNs),
})
async function take(gen, n) {
  const out = []
  for await (const x of gen) {
    if (typeof x.ack === 'function') x.ack() // durable: settle before leaving the loop, or it is redelivered after ack_wait
    out.push(x)
    if (out.length === n) break
  }
  return out
}
const timeout = (p, ms = 30000) => {
  let timer
  return Promise.race([p, new Promise((_, rej) => (timer = setTimeout(() => rej(new Error('timeout')), ms)))]).finally(() => clearTimeout(timer))
}

const t0 = Date.now()
const phase = (name) => console.error(`${name} +${Date.now() - t0}ms`)
const c = await connect({ servers: a.url, creds: readFileSync(a.creds, 'utf8'), source: 'ts-interop', ...(a.ca ? { ca: readFileSync(a.ca, 'utf8') } : {}) })
phase('connected')
const out = {}
const events = await timeout(take(c.subscribe('evt.interop.>', { since: a.since }), Number(a.count)))
out.events = events.map(evJSON)
for (const e of events) {
  if (e.blob) {
    const b = await c.getBlob(e.blob)
    out.blob_sha256 = Buffer.from(await crypto.subtle.digest('SHA-256', b)).toString('hex')
  }
}
phase('replayed')
const first = await timeout(take(c.subscribe('evt.interop.>', { durable: a.durable, since: a.since, ackWaitMs: 2000 }), 2))
const again = await timeout(take(c.subscribe('evt.interop.>', { durable: a.durable, ackWaitMs: 2000 }), 1))
out.durable = [...first, ...again].map((d) => d.sequence)
phase('durable')
const ref = await c.putBlob(new TextEncoder().encode('typescript blob '.repeat(20000)), '.bin')
const ack = await c.publish('evt.interop.ts.said', 'interop.said.v1', { lang: 'ts' }, { blob: ref, headers: { 'x-lang': 'ts' } })
out.published = { seq: ack.sequence, id: ack.id, blob: ref }
phase('published')
await c.close()
phase('closed')
console.log(JSON.stringify(out))
