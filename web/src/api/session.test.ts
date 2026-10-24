import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { CSRF_HEADER, isPublicPath, loginUrl, sessionFetch, setCsrfToken, setReauthHandler, setUnauthorizedHandler } from './session'

const problem = (status: number, code: string) =>
  new Response(JSON.stringify({ status, title: 'x', detail: code, code }), { status, headers: { 'Content-Type': 'application/problem+json' } })

let fetchMock: ReturnType<typeof vi.fn>

beforeEach(() => {
  fetchMock = vi.fn()
  vi.stubGlobal('fetch', fetchMock)
  setCsrfToken('csrf-1')
})

afterEach(() => {
  vi.unstubAllGlobals()
  setCsrfToken('')
})

test('state-changing requests carry the session CSRF token; reads do not', async () => {
  fetchMock.mockResolvedValue(new Response('{}', { status: 200 }))
  await sessionFetch('/api/admin/v1/streams', { method: 'POST', body: '{}' })
  await sessionFetch('/api/admin/v1/streams')
  expect((fetchMock.mock.calls[0][0] as Request).headers.get(CSRF_HEADER)).toBe('csrf-1')
  expect((fetchMock.mock.calls[1][0] as Request).headers.get(CSRF_HEADER)).toBeNull()
})

test('reauth_required runs the prompt and retries once with the rotated CSRF token and the same body', async () => {
  fetchMock.mockResolvedValueOnce(problem(401, 'reauth_required')).mockResolvedValueOnce(new Response('{"ok":true}', { status: 201 }))
  const prompt = vi.fn(async () => {
    setCsrfToken('csrf-2')
    return true
  })
  const off = setReauthHandler(prompt)
  const res = await sessionFetch('/api/admin/v1/clients', { method: 'POST', body: '{"name":"bot"}' })
  off()
  expect(prompt).toHaveBeenCalledWith('reauth_required')
  expect(res.status).toBe(201)
  const retry = fetchMock.mock.calls[1][0] as Request
  expect(retry.headers.get(CSRF_HEADER)).toBe('csrf-2')
  expect(await retry.text()).toBe('{"name":"bot"}')
})

test('a cancelled prompt returns the original 401 without retrying', async () => {
  fetchMock.mockResolvedValueOnce(problem(401, 'mfa_required'))
  const off = setReauthHandler(async () => false)
  const res = await sessionFetch('/api/admin/v1/backups/x/restore', { method: 'POST', body: '{}' })
  off()
  expect(res.status).toBe(401)
  expect(fetchMock).toHaveBeenCalledTimes(1)
})

test('concurrent step-up failures share one prompt', async () => {
  fetchMock.mockImplementation(async (r: Request) => (r.headers.get(CSRF_HEADER) === 'csrf-2' ? new Response('{}') : problem(401, 'reauth_required')))
  let calls = 0
  const off = setReauthHandler(async () => {
    calls++
    await new Promise((r) => setTimeout(r, 10))
    setCsrfToken('csrf-2')
    return true
  })
  const [a, b] = await Promise.all([
    sessionFetch('/api/admin/v1/a', { method: 'POST', body: '1' }),
    sessionFetch('/api/admin/v1/b', { method: 'POST', body: '2' }),
  ])
  off()
  expect(calls).toBe(1)
  expect([a.status, b.status]).toEqual([200, 200])
})

test('unauthorized outside /auth/ goes to sign-in; sign-in endpoints never redirect', async () => {
  const onUnauthorized = vi.fn()
  const off = setUnauthorizedHandler(onUnauthorized)
  fetchMock.mockResolvedValue(problem(401, 'unauthorized'))
  await sessionFetch('/api/admin/v1/overview')
  await sessionFetch('/api/admin/v1/auth/login', { method: 'POST', body: '{}' })
  off()
  expect(onUnauthorized).toHaveBeenCalledTimes(1)
})

test('sign-in step-up errors on /auth/ endpoints are not retried', async () => {
  fetchMock.mockResolvedValue(problem(401, 'reauth_required'))
  const prompt = vi.fn(async () => true)
  const off = setReauthHandler(prompt)
  await sessionFetch('/api/admin/v1/auth/reauth', { method: 'POST', body: '{}' })
  off()
  expect(prompt).not.toHaveBeenCalled()
})

test('public paths and login URLs', () => {
  for (const p of ['/login', '/setup', '/recover', '/invite/abc', '/reset/abc']) expect(isPublicPath(p)).toBe(true)
  for (const p of ['/', '/users', '/loginx', '/invite']) expect(isPublicPath(p)).toBe(false)
  expect(loginUrl('/explorer?x=1')).toBe('/login?return_to=%2Fexplorer%3Fx%3D1')
  expect(loginUrl('/')).toBe('/login')
})
