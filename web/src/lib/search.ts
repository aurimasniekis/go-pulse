import type { EventsQuery } from '@/api/queries'

const str = (v: unknown): string | undefined => (typeof v === 'string' && v.trim() !== '' ? v.trim() : undefined)
const strs = (v: unknown): string[] | undefined => {
  const arr = Array.isArray(v) ? v : typeof v === 'string' ? [v] : []
  const out = arr.filter((x): x is string => typeof x === 'string' && x.trim() !== '').map((x) => x.trim())
  return out.length ? out : undefined
}
const num = (v: unknown): number | undefined => {
  const n = typeof v === 'number' ? v : typeof v === 'string' ? Number(v) : NaN
  return Number.isFinite(n) && n > 0 ? Math.floor(n) : undefined
}

/** Parses explorer URL search params into a clean EventsQuery (unknown keys and empty values dropped). */
export function parseEventsSearch(s: Record<string, unknown>): EventsQuery {
  const q: EventsQuery = {
    stream: str(s.stream),
    subject: strs(s.subject),
    dir: s.dir === 'forward' ? 'forward' : s.dir === 'backward' ? 'backward' : undefined,
    start_seq: num(s.start_seq),
    start_time: str(s.start_time),
    type: str(s.type),
    header: strs(s.header),
    contains: str(s.contains),
    json_path: str(s.json_path),
    json_value: str(s.json_value),
  }
  for (const k of Object.keys(q) as (keyof EventsQuery)[]) if (q[k] === undefined) delete q[k]
  return q
}

const sizeUnits: Record<string, number> = { '': 1, b: 1, k: 1e3, kb: 1e3, m: 1e6, mb: 1e6, g: 1e9, gb: 1e9, t: 1e12, tb: 1e12, kib: 1024, mib: 1024 ** 2, gib: 1024 ** 3, tib: 1024 ** 4 }

/** Parses "10GiB", "500 MB", "1048576"; empty → undefined; "-1"/"unlimited" → -1. Throws on garbage. */
export function parseSize(input: string): number | undefined {
  const s = input.trim().toLowerCase()
  if (s === '') return undefined
  if (s === '-1' || s === 'unlimited' || s === '∞') return -1
  const m = /^(\d+(?:\.\d+)?)\s*([a-z]*)$/.exec(s)
  if (!m || !(m[2] in sizeUnits)) throw new Error(`invalid size "${input}"`)
  return Math.round(Number(m[1]) * sizeUnits[m[2]])
}

const durUnits: Record<string, number> = { s: 1, m: 60, h: 3600, d: 86400, w: 604800, y: 31536000 }

/** Parses "30d", "12h", "1y", "90" (seconds); empty or "0"/"forever" → 0. */
export function parseDuration(input: string): number {
  const s = input.trim().toLowerCase()
  if (s === '' || s === '0' || s === 'forever') return 0
  const m = /^(\d+)\s*([smhdwy]?)$/.exec(s)
  if (!m) throw new Error(`invalid duration "${input}"`)
  return Number(m[1]) * (m[2] ? durUnits[m[2]] : 1)
}
