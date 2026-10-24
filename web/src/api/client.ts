import createClient from 'openapi-fetch'
import type { paths as AdminPaths, components as AdminComponents } from './admin'
import { sessionMiddleware } from './session'

export { CSRF_HEADER } from './session'

/** Typed admin API client; types are generated from api/admin.openapi.json (make api). */
export const admin = createClient<AdminPaths>({ baseUrl: '/api/admin/v1' })
admin.use(sessionMiddleware)

export type Schemas = AdminComponents['schemas']
export type EventDTO = Schemas['EventDTO']
export type StreamDTO = Schemas['StreamDTO']
export type ConsumerDTO = Schemas['ConsumerDTO']
export type EventsPage = Schemas['EventsPage']
export type SystemInfo = Schemas['SystemInfo']

/** Problem details (RFC 9457) returned by the API; `code` is set by the sign-in layer (reauth_required, …). */
export interface Problem {
  title?: string
  status?: number
  detail?: string
  code?: string
  errors?: { message?: string; location?: string }[]
}

/** An API error with its HTTP status and stable code. */
export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly code: string = '',
  ) {
    super(message)
    this.name = 'ApiError'
  }
}

/** Builds an ApiError from a problem body. */
export function apiError(problem: unknown, status: number): ApiError {
  const p = (problem ?? {}) as Problem
  const msg = p.detail || p.title || `HTTP ${status}`
  const extra = p.errors?.map((e) => e.message).filter(Boolean).join('; ')
  return new ApiError(extra ? `${msg}: ${extra}` : msg, status, p.code ?? '')
}

/** Unwraps an openapi-fetch result or throws an ApiError. */
export function unwrap<T>(res: { data?: T; error?: unknown; response: Response }): T {
  if (res.error !== undefined || res.data === undefined) throw apiError(res.error, res.response.status)
  return res.data
}

/** Throws an ApiError for a failed result without a body (204 endpoints). */
export function check(res: { error?: unknown; response: Response }): void {
  if (res.error !== undefined || !res.response.ok) throw apiError(res.error, res.response.status)
}

export type Me = Schemas['Me']
export type User = Schemas['User']
export type AuthConfig = Schemas['AuthConfig']
export type SessionInfo = Schemas['SessionInfo']
export type PAT = Schemas['PAT']
export type Invitation = Schemas['Invitation']
export type OneTimeLink = Schemas['OneTimeLink']
export type SSOProvider = Schemas['SSOProvider']
export type SSOMapping = Schemas['Mapping']
export type Identity = Schemas['Identity']
export type ProviderInput = Schemas['ProviderInput']
export type TestResult = Schemas['TestResult']

export type Grant = Schemas['Grant']
export type ClientDTO = Schemas['ClientDTO']
export type ConnDTO = Schemas['ConnDTO']
export type TokenDTO = Schemas['Token']
export type AuditEntry = Schemas['Entry']
export type InboundHook = Schemas['InboundHook']
export type OutboundHook = Schemas['Hook']
export type Delivery = Schemas['Delivery']
