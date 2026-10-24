import { useQuery, useQueryClient } from '@tanstack/react-query'
import { admin, apiError, type Me } from './client'
import { setCsrfToken } from './session'

export const meKey = ['me'] as const

/** GET /auth/me; keeps the CSRF token in sync. Resolves to null without a session. */
export async function fetchMe(): Promise<Me | null> {
  const res = await admin.GET('/auth/me')
  if (res.response.status === 401) {
    setCsrfToken('')
    return null
  }
  if (res.error !== undefined || !res.data) throw apiError(res.error, res.response.status)
  setCsrfToken(res.data.csrf_token)
  return res.data
}

export function useMe() {
  return useQuery({ queryKey: meKey, queryFn: fetchMe, retry: false, staleTime: 30_000, refetchInterval: 60_000 })
}

/** Stores a freshly returned Me (after login, MFA, reauth) without another round trip. */
export function useSetMe() {
  const qc = useQueryClient()
  return (me: Me | null) => {
    setCsrfToken(me?.csrf_token)
    qc.setQueryData(meKey, me)
  }
}

export function useAuthConfig() {
  return useQuery({
    queryKey: ['auth-config'],
    queryFn: async () => {
      const res = await admin.GET('/auth/config')
      if (res.error !== undefined || !res.data) throw apiError(res.error, res.response.status)
      return res.data
    },
    retry: false,
    staleTime: 0,
  })
}

/** Permission names (internal/rbac). */
export type Perm =
  | 'dashboard.read'
  | 'events.read'
  | 'events.publish'
  | 'streams.write'
  | 'clients.read'
  | 'clients.write'
  | 'webhooks.read'
  | 'webhooks.write'
  | 'security.read'
  | 'security.write'
  | 'alerts.read'
  | 'alerts.write'
  | 'backups.read'
  | 'backups.run'
  | 'backups.restore'
  | 'audit.read'
  | 'system.read'
  | 'users.read'
  | 'users.manage'
  | 'auth.settings'

export function hasPerm(me: Me | null | undefined, perm: Perm): boolean {
  return !!me && !me.mfa_pending && !me.must_enroll && me.permissions.includes(perm)
}

/** Whether the signed-in user holds perm (the server enforces it; this only hides what would fail). */
export function useCan(perm: Perm): boolean {
  const { data } = useMe()
  return hasPerm(data, perm)
}

export const ROLES = ['administrator', 'operator', 'auditor', 'viewer'] as const
export type Role = (typeof ROLES)[number]

export const roleInfo: Record<Role, { color: string; description: string }> = {
  administrator: { color: 'red', description: 'Everything, including users, single sign-on and restores' },
  operator: { color: 'indigo', description: 'Runs the bus: clients, tokens, webhooks, streams, bans, alerts, backups' },
  auditor: { color: 'grape', description: 'Reads everything, including the audit log and users' },
  viewer: { color: 'gray', description: 'Reads dashboards, events and status' },
}
