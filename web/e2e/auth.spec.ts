import { readFileSync } from 'node:fs'
import { join } from 'node:path'

import { expect, test } from '@playwright/test'

import { csrf } from './admin'
import { authDir } from './paths'
import { totp } from './totp'

// Dashboard sign-in through the browser (ADR-0038): invitation → password → sign in as a viewer, role-based
// navigation, sign-out; then the e2e administrator with password and TOTP.
test.use({ storageState: { cookies: [], origins: [] } })

test('invitation, viewer sign-in, gated navigation, sign-out', async ({ page, request, browser }) => {
  const admin = await browser.newContext({ storageState: join(authDir, 'state.json') })
  const email = `viewer-${Date.now().toString(36)}@example.com`
  const inv = await admin.request.post('/api/admin/v1/invitations', { headers: csrf, data: { email, roles: ['viewer'] } })
  expect(inv.ok(), await inv.text()).toBeTruthy()
  const { url } = (await inv.json()) as { url: string }
  await admin.close()

  await page.goto(url)
  const form = page.getByRole('form', { name: 'Accept invitation' })
  await form.getByRole('textbox', { name: 'Display name' }).fill('Vic Viewer')
  await form.getByRole('textbox', { name: 'Password', exact: true }).fill('viewer password 123')
  await form.getByRole('textbox', { name: 'Confirm password' }).fill('viewer password 123')
  await form.getByRole('button', { name: 'Create account' }).click()
  await page.getByRole('button', { name: 'Sign in' }).click()

  const login = page.getByRole('form', { name: 'Sign in with email and password' })
  await login.getByRole('textbox', { name: 'Email' }).fill(email)
  await login.getByRole('textbox', { name: 'Password' }).fill('viewer password 123')
  await login.getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByRole('heading', { name: 'Overview', exact: true })).toBeVisible()
  // Viewers see dashboards, not access management.
  await expect(page.getByRole('link', { name: 'Explorer' })).toBeVisible()
  await expect(page.getByRole('link', { name: 'Audit' })).toHaveCount(0)
  await expect(page.getByRole('link', { name: 'Users' })).toHaveCount(0)
  await page.getByTestId('user-menu').click()
  await page.getByRole('menuitem', { name: 'Sign out' }).click()
  await expect(page.getByRole('form', { name: 'Sign in with email and password' })).toBeVisible()
  // The old session is gone.
  expect((await request.get('/api/admin/v1/overview')).status()).toBe(401)
})

test('administrator signs in with password and TOTP', async ({ page }) => {
  const saved = JSON.parse(readFileSync(join(authDir, 'admin.json'), 'utf8')) as { email: string; password: string; totp: string }
  await page.goto('/explorer')
  await expect(page).toHaveURL(/\/login/)
  const login = page.getByRole('form', { name: 'Sign in with email and password' })
  await login.getByRole('textbox', { name: 'Email' }).fill(saved.email)
  await login.getByRole('textbox', { name: 'Password' }).fill(saved.password)
  await login.getByRole('button', { name: 'Sign in' }).click()
  const code = page.getByRole('textbox', { name: 'Authentication code' })
  await expect(code).toBeVisible()
  // A code may already have been used in this 30 s step (global setup): use the next step's code once it is valid.
  const step = Math.floor(Date.now() / 30_000)
  await page.waitForTimeout((step + 1) * 30_000 - Date.now() + 300)
  await code.fill(totp(saved.totp))
  await page.getByRole('button', { name: 'Verify' }).click()
  await expect(page.getByRole('heading', { name: 'Explorer', exact: true })).toBeVisible()
  await expect(page.getByRole('link', { name: 'Users' })).toBeVisible()
})
