import type { ReactNode } from 'react'
import { useCan, type Perm } from '@/api/auth'

/** Renders children only when the signed-in user holds perm (the server enforces it; this hides what would fail). */
export function Can({ perm, children }: { perm: Perm; children: ReactNode }) {
  return useCan(perm) ? <>{children}</> : null
}
