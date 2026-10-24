import type { Middleware } from 'openapi-fetch'

/**
 * Dashboard session plumbing (ADR-0038). The session cookie is HttpOnly; what the page holds is the session's CSRF
 * token (from GET /auth/me), sent as X-Pulse-CSRF on every state-changing request.
 *
 * - `reauth_required` / `mfa_required` (401): the registered re-authentication prompt (ReauthModal) runs, the session
 *   rotates (new CSRF token), and the original request is retried exactly once. Concurrent requests share one prompt.
 * - `unauthorized` (401) outside /auth/*: the session is gone; the unauthorized handler sends the browser to /login.
 */

export const CSRF_HEADER = 'X-Pulse-CSRF'

export type ReauthCode = 'reauth_required' | 'mfa_required'
export type ReauthHandler = (code: ReauthCode) => Promise<boolean>

const AUTH_PREFIX = '/api/admin/v1/auth/'

let csrf = ''
let reauthHandler: ReauthHandler | null = null
let unauthorizedHandler: () => void = defaultUnauthorized
let inflight: Promise<boolean> | null = null

export function setCsrfToken(token: string | undefined | null) {
  csrf = token ?? ''
}

export function csrfToken(): string {
  return csrf
}

/** Registers the re-authentication prompt; returns an unregister function. */
export function setReauthHandler(h: ReauthHandler): () => void {
  reauthHandler = h
  return () => {
    if (reauthHandler === h) reauthHandler = null
  }
}

export function setUnauthorizedHandler(h: () => void): () => void {
  unauthorizedHandler = h
  return () => {
    if (unauthorizedHandler === h) unauthorizedHandler = defaultUnauthorized
  }
}

/** Paths that render without a session (the sign-in pages). */
export const PUBLIC_PATHS = ['/login', '/setup', '/recover', '/invite/', '/reset/']

export function isPublicPath(path: string): boolean {
  return PUBLIC_PATHS.some((p) => (p.endsWith('/') ? path.startsWith(p) : path === p))
}

export function loginUrl(returnTo: string): string {
  return returnTo && returnTo !== '/' ? `/login?return_to=${encodeURIComponent(returnTo)}` : '/login'
}

function defaultUnauthorized() {
  const here = `${window.location.pathname}${window.location.search}`
  if (!isPublicPath(window.location.pathname)) window.location.assign(loginUrl(here))
}

const mutating = (method: string) => !['GET', 'HEAD', 'OPTIONS'].includes(method.toUpperCase())

/** The `code` of a problem response, or "". */
export async function problemCode(res: Response): Promise<string> {
  try {
    const body = (await res.clone().json()) as { code?: unknown }
    return typeof body.code === 'string' ? body.code : ''
  } catch {
    return ''
  }
}

function prompt(code: ReauthCode): Promise<boolean> {
  if (!reauthHandler) return Promise.resolve(false)
  inflight ??= reauthHandler(code).finally(() => {
    inflight = null
  })
  return inflight
}

function prepare(request: Request): Request {
  if (mutating(request.method) && csrf) request.headers.set(CSRF_HEADER, csrf)
  return request
}

/** Handles a 401 for request (original is an unconsumed clone made before sending). */
async function after(original: Request | undefined, request: Request, response: Response, doFetch: typeof fetch): Promise<Response> {
  if (response.status !== 401) return response
  const path = new URL(request.url, window.location.origin).pathname
  const code = await problemCode(response)
  if ((code === 'reauth_required' || code === 'mfa_required') && original && !path.startsWith(AUTH_PREFIX)) {
    if (!(await prompt(code))) return response
    const retry = original.clone()
    if (mutating(retry.method)) retry.headers.set(CSRF_HEADER, csrf)
    return doFetch(retry)
  }
  if (code === 'unauthorized' && !path.startsWith(AUTH_PREFIX)) unauthorizedHandler()
  return response
}

const originals = new WeakMap<Request, Request>()

/** openapi-fetch middleware for the admin client. */
export const sessionMiddleware: Middleware = {
  onRequest({ request }) {
    prepare(request)
    originals.set(request, request.clone())
    return request
  },
  async onResponse({ request, response }) {
    return after(originals.get(request), request, response, (r) => fetch(r))
  },
}

/** fetch with the same session handling, for requests outside the typed client (raw uploads). */
export async function sessionFetch(input: string, init: RequestInit = {}): Promise<Response> {
  const request = prepare(new Request(new URL(input, window.location.origin), init))
  const original = request.clone()
  const response = await fetch(request)
  return after(original, request, response, (r) => fetch(r))
}
