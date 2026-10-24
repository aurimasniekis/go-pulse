import type { Schemas } from '@/api/client'

export type AlertRule = Schemas['AlertRule']
export type AlertParams = Schemas['RuleParams']

/** Severity badge colour and label; the label always names the severity (never colour alone). */
export const severityColor: Record<string, string> = { critical: 'red', warning: 'orange', info: 'blue' }

/** One-line summary of a rule's thresholds for the rules table. */
export function paramSummary(kind: string, p: AlertParams): string {
  switch (kind) {
    case 'disk_forecast':
      return `full within ${p.days} d`
    case 'stream_usage':
      return `> ${Math.round((p.ratio ?? 0) * 100)}% of max_bytes`
    case 'cert_expiry':
    case 'client_expiry':
      return `< ${p.days} d left`
    case 'backup_stale':
      return `no success for ${p.hours} h`
    case 'ban_storm':
      return `≥ ${p.count} bans / ${p.window_minutes} min`
    case 'slow_consumers':
      return `new slow consumers / ${p.window_minutes} min`
    case 'dead_letters':
      return `dead letters in ${p.hours} h`
    case 'nats_down':
      return 'disconnected'
    case 'threshold': {
      const scope = p.dimension === 'total' ? 'all events' : p.dimension === 'type' ? `type ${p.key}` : `${p.key}.>`
      return `${scope} ${p.op === 'below' ? '<' : '>'} ${p.threshold} ${p.metric === 'bytes' ? 'bytes' : 'events'} / ${p.window_minutes} min`
    }
    default:
      return ''
  }
}

/** Which numeric params a built-in kind exposes in the editor. */
export const kindFields: Record<string, { key: keyof AlertParams; label: string; step?: number; min?: number; max?: number }[]> = {
  disk_forecast: [{ key: 'days', label: 'Days until full', min: 1 }],
  stream_usage: [{ key: 'ratio', label: 'Fraction of max_bytes', step: 0.01, min: 0.01, max: 0.99 }],
  cert_expiry: [{ key: 'days', label: 'Days left', min: 1 }],
  client_expiry: [{ key: 'days', label: 'Days left', min: 1 }],
  backup_stale: [{ key: 'hours', label: 'Hours without a successful backup', min: 1 }],
  ban_storm: [
    { key: 'count', label: 'Bans', min: 1 },
    { key: 'window_minutes', label: 'Window (minutes)', min: 1 },
  ],
  slow_consumers: [{ key: 'window_minutes', label: 'Window (minutes)', min: 1 }],
  dead_letters: [{ key: 'hours', label: 'Look-back (hours)', min: 1 }],
  nats_down: [],
}
