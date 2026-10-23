import type { Schemas } from '@/api/client'

type Cert = Pick<Schemas['CertificateInfo'], 'present' | 'days_left'>

/** Expiry badge: the colour follows the remaining validity and the label states it (never colour alone). */
export function expiryBadge(c: Cert): { color: string; label: string } {
  if (!c.present) return { color: 'gray', label: 'not present' }
  if (c.days_left < 0) return { color: 'red', label: 'expired' }
  if (c.days_left < 14) return { color: 'red', label: `${c.days_left}d left` }
  if (c.days_left < 30) return { color: 'yellow', label: `${c.days_left}d left` }
  return { color: 'teal', label: `${c.days_left}d left` }
}
