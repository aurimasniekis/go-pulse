import { expect, test } from '@playwright/test'

const publicURL = process.env.PULSE_E2E_PUBLIC_URL ?? 'http://127.0.0.1:8443'

test('security: failures are recorded; ban, lift, allow and remove', async ({ page, request }) => {
  // A bad token from the host is a private address: recorded, never banned.
  const r = await request.post(`${publicURL}/api/v1/publish/evt.sec.a.b`, { headers: { Authorization: 'Bearer pulse_aaaaaaaa_bad' }, data: 'x' })
  expect(r.status()).toBe(401)

  await page.goto('/security')
  await page.getByRole('tab', { name: /Recent failures/ }).click()
  await expect(page.getByTestId('failures-table')).toContainText('http_token')
  await expect(page.getByTestId('failures-table')).toContainText('never-ban network')

  await page.getByRole('button', { name: 'Ban network' }).click()
  const dialog = page.getByRole('dialog', { name: 'Ban network' })
  await dialog.getByLabel('IP or CIDR').fill('203.0.113.77')
  await dialog.getByLabel('Reason').fill('e2e scanner')
  await dialog.getByRole('button', { name: 'Ban' }).click()
  const bans = page.getByTestId('bans-table')
  await expect(bans).toContainText('203.0.113.77/32')
  await expect(bans).toContainText('e2e scanner')
  await page.getByRole('button', { name: 'Lift ban on 203.0.113.77/32' }).click()
  await expect(bans).not.toContainText('203.0.113.77/32')

  await page.getByRole('button', { name: 'Ban network' }).click()
  await dialog.getByLabel('IP or CIDR').fill('10.1.2.3')
  await dialog.getByRole('button', { name: 'Ban' }).click()
  await expect(page.getByText(/may not be banned/)).toBeVisible()
  await page.keyboard.press('Escape')

  await page.getByRole('button', { name: 'Allow network' }).click()
  const allow = page.getByRole('dialog', { name: 'Allow network' })
  await allow.getByLabel('IP or CIDR').fill('198.51.100.0/24')
  await allow.getByLabel('Note').fill('e2e office')
  await allow.getByRole('button', { name: 'Allow' }).click()
  await page.getByRole('tab', { name: /Allowlist/ }).click()
  await expect(page.getByTestId('allow-table')).toContainText('198.51.100.0/24')
  await page.getByRole('button', { name: 'Remove 198.51.100.0/24 from the allowlist' }).click()
  await expect(page.getByTestId('allow-table')).not.toContainText('198.51.100.0/24')
})
