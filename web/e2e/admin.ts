/**
 * Headers for admin API calls from tests through Playwright's `request` fixture: the signed-in e2e session's CSRF
 * token (global-setup.ts) and the dashboard Origin, which the admin API requires on state-changing requests.
 */
export const csrf: Record<string, string> = {
  'X-Pulse-CSRF': process.env.PULSE_E2E_CSRF ?? '',
  Origin: process.env.PULSE_E2E_URL ?? 'http://127.0.0.1:8080',
}
