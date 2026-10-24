import { execFileSync } from 'node:child_process'
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

import { request, type APIRequestContext, type FullConfig } from '@playwright/test'

import { adminURL as base, authDir, stateFile } from './paths'
import { totp } from './totp'

/**
 * Signs in an e2e administrator (ADR-0038) and saves the browser session for every test:
 * - a fresh stack: `pulsed admin bootstrap` → setup page API → password;
 * - a stack whose e2e administrator exists: its saved password and TOTP secret;
 * - otherwise: `pulsed admin recover --new-admin` (break-glass) for a new e2e administrator.
 * Then TOTP enrollment (administrators must enroll) and the second factor. The CSRF token goes to PULSE_E2E_CSRF.
 */
const secretFile = join(authDir, 'admin.json')
// The pulsed CLI inside the dev stack (override for other setups).
const pulsedCmd = (process.env.PULSE_E2E_PULSED ?? 'docker compose -f ../deploy/compose.dev.yaml -p pulse-dev exec -T pulsed pulsed').split(' ')

interface Saved {
  email: string
  password: string
  totp?: string
}


function pulsed(...args: string[]): string {
  return execFileSync(pulsedCmd[0], [...pulsedCmd.slice(1), ...args], { encoding: 'utf8' })
}

function tokenFrom(out: string): string {
  const m = out.match(/token: (\S+)/)
  if (!m) throw new Error(`no token in:\n${out}`)
  return m[1]
}

async function call(api: APIRequestContext, method: string, path: string, data?: unknown, csrf = '') {
  const res = await api.fetch(`/api/admin/v1${path}`, {
    method,
    data,
    headers: { Origin: base, ...(csrf ? { 'X-Pulse-CSRF': csrf } : {}) },
  })
  const body = res.status() === 204 ? {} : await res.json().catch(() => ({}))
  return { ok: res.ok(), status: res.status(), body }
}

async function waitNextStep() {
  const ms = 30_000 - (Date.now() % 30_000) + 500
  await new Promise((r) => setTimeout(r, ms))
}

export default async function globalSetup(_: FullConfig) {
  mkdirSync(authDir, { recursive: true })
  const api = await request.newContext({ baseURL: base })
  let saved: Saved | undefined
  try {
    saved = JSON.parse(readFileSync(secretFile, 'utf8')) as Saved
  } catch {
    saved = undefined
  }
  const cfg = (await call(api, 'GET', '/auth/config')).body as { bootstrap_required?: boolean }
  let login = saved ? await call(api, 'POST', '/auth/login', { email: saved.email, password: saved.password }) : undefined
  if (!login?.ok) {
    // scripts/screenshots.sh picks a presentable administrator; e2e runs use a throwaway address.
    const email = process.env.PULSE_E2E_ADMIN_EMAIL ?? `e2e-${Date.now().toString(36)}@example.com`
    const password = `e2e-${Math.random().toString(36).slice(2)}-${Math.random().toString(36).slice(2)}`
    if (cfg.bootstrap_required) {
      const token = tokenFrom(pulsed('admin', 'bootstrap'))
      const r = await call(api, 'POST', '/auth/setup', { token, email, display_name: process.env.PULSE_E2E_ADMIN_NAME ?? 'E2E admin', password })
      if (!r.ok) throw new Error(`setup: ${r.status} ${JSON.stringify(r.body)}`)
    } else {
      const token = tokenFrom(pulsed('admin', 'recover', '--email', email, '--new-admin'))
      const rec = await call(api, 'POST', '/auth/recover', { token })
      const reset = (rec.body as { password_reset?: { token: string } }).password_reset
      if (!rec.ok || !reset) throw new Error(`recover: ${rec.status} ${JSON.stringify(rec.body)}`)
      const r = await call(api, 'POST', `/auth/reset/${reset.token}`, { password })
      if (!r.ok) throw new Error(`reset: ${r.status} ${JSON.stringify(r.body)}`)
    }
    saved = { email, password }
    login = await call(api, 'POST', '/auth/login', { email, password })
    if (!login.ok) throw new Error(`login: ${login.status} ${JSON.stringify(login.body)}`)
  }
  if (!saved) throw new Error('no e2e administrator')
  let me = (login.body as { me: { csrf_token: string } }).me
  const state = login.body as { must_enroll: boolean; mfa_pending: boolean }
  if (state.must_enroll || !saved.totp) {
    const begin = await call(api, 'POST', '/account/totp/begin', undefined, me.csrf_token)
    if (!begin.ok) throw new Error(`totp begin: ${begin.status} ${JSON.stringify(begin.body)}`)
    saved.totp = (begin.body as { secret: string }).secret
    const confirm = await call(api, 'POST', '/account/totp/confirm', { code: totp(saved.totp) }, me.csrf_token)
    if (!confirm.ok) throw new Error(`totp confirm: ${confirm.status} ${JSON.stringify(confirm.body)}`)
    writeFileSync(secretFile, JSON.stringify(saved), { mode: 0o600 })
    await waitNextStep()
  }
  me = (await call(api, 'GET', '/auth/me')).body as typeof me & { mfa_pending: boolean }
  if ((me as { mfa_pending?: boolean }).mfa_pending) {
    let r = await call(api, 'POST', '/auth/mfa', { code: totp(saved.totp!) }, me.csrf_token)
    if (r.status === 401) {
      await waitNextStep() // the code of this step was used already
      r = await call(api, 'POST', '/auth/mfa', { code: totp(saved.totp!) }, me.csrf_token)
    }
    if (!r.ok) throw new Error(`mfa: ${r.status} ${JSON.stringify(r.body)}`)
    me = r.body as typeof me
  }
  writeFileSync(secretFile, JSON.stringify(saved), { mode: 0o600 })
  await api.storageState({ path: stateFile })
  process.env.PULSE_E2E_CSRF = me.csrf_token
  process.env.PULSE_E2E_ADMIN_EMAIL = saved.email
  await api.dispose()
}
