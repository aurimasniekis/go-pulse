import createClient, { type Middleware } from 'openapi-fetch'
import type { paths as AdminPaths, components as AdminComponents } from './admin'

/** Header the admin API requires on state-changing requests (PL-ADM-004). */
export const CSRF_HEADER = 'X-Pulse-CSRF'

const csrf: Middleware = {
  onRequest({ request }) {
    if (!['GET', 'HEAD', 'OPTIONS'].includes(request.method)) {
      request.headers.set(CSRF_HEADER, '1')
    }
    return request
  },
}

/** Typed admin API client; types are generated from api/admin.openapi.json (make api). */
export const admin = createClient<AdminPaths>({ baseUrl: '/api/admin/v1' })
admin.use(csrf)

export type Schemas = AdminComponents['schemas']
export type EventDTO = Schemas['EventDTO']
export type StreamDTO = Schemas['StreamDTO']
export type ConsumerDTO = Schemas['ConsumerDTO']
export type EventsPage = Schemas['EventsPage']
export type SystemInfo = Schemas['SystemInfo']

/** Problem details (RFC 9457) returned by the API. */
export interface Problem {
  title?: string
  status?: number
  detail?: string
  errors?: { message?: string; location?: string }[]
}

/** Unwraps an openapi-fetch result or throws a readable error. */
export function unwrap<T>(res: { data?: T; error?: unknown; response: Response }): T {
  if (res.error !== undefined || res.data === undefined) {
    const p = (res.error ?? {}) as Problem
    const msg = p.detail || p.title || `HTTP ${res.response.status}`
    const extra = p.errors?.map((e) => e.message).filter(Boolean).join('; ')
    throw new Error(extra ? `${msg}: ${extra}` : msg)
  }
  return res.data
}

export type Grant = Schemas['Grant']
export type ClientDTO = Schemas['ClientDTO']
export type ConnDTO = Schemas['ConnDTO']
export type TokenDTO = Schemas['Token']
export type AuditEntry = Schemas['Entry']
