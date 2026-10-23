/** Formatting helpers shared by tables, charts and detail views. */

const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB']

export function formatBytes(n: number | undefined | null, digits = 1): string {
  if (n === undefined || n === null || !Number.isFinite(n)) return '—'
  if (n < 0) return '∞'
  let i = 0
  let v = n
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${i === 0 ? v : v.toFixed(digits)} ${units[i]}`
}

const nf = new Intl.NumberFormat('en-US')

export function formatNumber(n: number | undefined | null): string {
  if (n === undefined || n === null || !Number.isFinite(n)) return '—'
  if (n < 0) return '∞'
  return nf.format(n)
}

/** Compact counts: 1.2k, 3.4M. */
export function formatCompact(n: number | undefined | null): string {
  if (n === undefined || n === null || !Number.isFinite(n)) return '—'
  return new Intl.NumberFormat('en-US', { notation: 'compact', maximumFractionDigits: 1 }).format(n)
}

export function formatDuration(seconds: number | undefined | null): string {
  if (seconds === undefined || seconds === null) return '—'
  if (seconds <= 0) return 'forever'
  const parts: [number, string][] = [
    [86400, 'd'],
    [3600, 'h'],
    [60, 'm'],
    [1, 's'],
  ]
  const out: string[] = []
  let rest = Math.floor(seconds)
  for (const [size, unit] of parts) {
    if (rest >= size) {
      out.push(`${Math.floor(rest / size)}${unit}`)
      rest %= size
    }
    if (out.length === 2) break
  }
  return out.join(' ') || '0s'
}

/** Absolute timestamp in local time with milliseconds, e.g. 2026-10-23 21:04:05.123. */
export function formatTime(iso: string | undefined | null): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime()) || d.getFullYear() < 1971) return '—'
  const p = (n: number, w = 2) => String(n).padStart(w, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}.${p(d.getMilliseconds(), 3)}`
}

/** Relative time: "5s ago", "3m ago", "2h ago", "4d ago". */
export function timeAgo(iso: string | undefined | null, now = Date.now()): string {
  if (!iso) return '—'
  const t = new Date(iso).getTime()
  if (Number.isNaN(t) || t < 31536000000) return '—'
  const s = Math.round((now - t) / 1000)
  if (s < 0) return 'just now'
  if (s < 60) return `${s}s ago`
  if (s < 3600) return `${Math.floor(s / 60)}m ago`
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`
  return `${Math.floor(s / 86400)}d ago`
}

/** Decodes an API payload (utf8 or base64) into bytes. */
export function payloadBytes(data: string, encoding: 'utf8' | 'base64'): Uint8Array {
  if (encoding === 'utf8') return new TextEncoder().encode(data)
  const bin = atob(data)
  const out = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i)
  return out
}

/** Pretty JSON if the text parses, otherwise undefined. */
export function prettyJSON(text: string): string | undefined {
  const t = text.trim()
  if (!(t.startsWith('{') || t.startsWith('['))) return undefined
  try {
    return JSON.stringify(JSON.parse(t), null, 2)
  } catch {
    return undefined
  }
}

/** Classic hex dump: offset, 16 bytes of hex, ASCII column. */
export function hexDump(bytes: Uint8Array, max = 4096): string {
  const lines: string[] = []
  const n = Math.min(bytes.length, max)
  for (let off = 0; off < n; off += 16) {
    const chunk = bytes.slice(off, Math.min(off + 16, n))
    const hex = Array.from(chunk, (b) => b.toString(16).padStart(2, '0')).join(' ')
    const ascii = Array.from(chunk, (b) => (b >= 32 && b < 127 ? String.fromCharCode(b) : '.')).join('')
    lines.push(`${off.toString(16).padStart(8, '0')}  ${hex.padEnd(47)}  ${ascii}`)
  }
  if (bytes.length > max) lines.push(`… ${bytes.length - max} more bytes`)
  return lines.join('\n')
}

/** One-line preview of a payload for tables. */
export function preview(data: string, encoding: 'utf8' | 'base64', max = 160): string {
  if (encoding === 'base64') return `<${payloadBytes(data, encoding).length} bytes binary>`
  const one = data.replace(/\s+/g, ' ').trim()
  return one.length > max ? `${one.slice(0, max)}…` : one
}

/** Shell-quotes a value for copy-paste commands. */
export function shellQuote(s: string): string {
  return /^[A-Za-z0-9_./:=@%+-]+$/.test(s) ? s : `'${s.replace(/'/g, `'\\''`)}'`
}
