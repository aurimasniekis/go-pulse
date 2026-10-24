import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

/** The admin URL the e2e run targets (the dev stack by default). */
export const adminURL = process.env.PULSE_E2E_URL ?? 'http://127.0.0.1:8080'

/** Saved sign-in per stack (gitignored): the e2e administrator's password and TOTP secret, and the browser session. */
export const authDir = join(dirname(fileURLToPath(import.meta.url)), '.auth', new URL(adminURL).host.replace(/[^a-z0-9.-]/gi, '_'))
export const stateFile = join(authDir, 'state.json')
