import { expect, test } from '@playwright/test'

// PL-UI-004 / PL-UI-009: every page renders against the dev stack without errors.
// Keep in sync with src/lib/nav.ts (nav.test.ts checks every nav entry has a route).
const pages: [string, string][] = [
  ['/', 'Overview'],
  ['/explorer', 'Explorer'],
  ['/tail', 'Live tail'],
  ['/subjects', 'Subjects'],
  ['/streams', 'Streams'],
  ['/kv', 'KV & Objects'],
  ['/kv?tab=objects', 'KV & Objects'],
  ['/clients', 'Clients'],
  ['/tokens', 'HTTP tokens'],
  ['/webhooks', 'Webhooks'],
  ['/security', 'Security'],
  ['/audit', 'Audit'],
  ['/system', 'System'],
  ['/alerts', 'Alerts'],
  ['/backups', 'Backups'],
  ['/settings', 'Settings'],
  ['/settings?tab=sso', 'Settings'],
  ['/users', 'Users'],
  ['/users?tab=invitations', 'Users'],
  ['/account', 'Account'],
]

for (const [path, title] of pages) {
  test(`page ${path} renders`, async ({ page }) => {
    const problems: string[] = []
    page.on('pageerror', (e) => problems.push(`pageerror: ${e.message}`))
    page.on('console', (m) => {
      if (m.type() === 'error') problems.push(`console: ${m.text()}`)
    })
    await page.goto(path)
    await expect(page.getByRole('heading', { name: title, exact: true }).first()).toBeVisible()
    await page.waitForLoadState('networkidle')
    await expect(page.getByText('Request failed')).toHaveCount(0)
    expect(problems).toEqual([])
  })
}
