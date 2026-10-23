import { expect, test } from '@playwright/test'

const run = Date.now().toString(36)
const publicURL = process.env.PULSE_E2E_PUBLIC_URL ?? 'http://127.0.0.1:8443'

test('inbound hook: create with a shared secret, deliver, see the event', async ({ page, request }) => {
  const name = `in-${run}`
  const secret = 'e2e-shared-secret-value'
  await page.goto('/webhooks')
  await page.getByRole('button', { name: 'Create inbound hook' }).click()
  const dialog = page.getByRole('dialog', { name: 'Create inbound hook' })
  await dialog.getByLabel('Name').fill(name)
  await dialog.getByRole('textbox', { name: 'Verifier' }).click()
  await page.getByRole('option', { name: 'Shared secret header' }).click()
  await dialog.getByLabel('Secret').fill(secret)
  await dialog.getByRole('button', { name: 'Create' }).click()
  await expect(page.getByTestId('inbound-table')).toContainText(`/hooks/${name}`)

  const bad = await request.post(`${publicURL}/hooks/${name}`, { headers: { 'X-Webhook-Secret': 'wrong' }, data: { a: 1 } })
  expect(bad.status()).toBe(401)
  const ok = await request.post(`${publicURL}/hooks/${name}`, { headers: { 'X-Webhook-Secret': secret }, data: { alarm: 'kitchen' } })
  expect(ok.status()).toBe(202)

  await page.goto(`/explorer?subject=evt.hook.${name}.received`)
  await expect(page.getByTestId('events-table').getByRole('row')).toHaveCount(1)
  await expect(page.getByTestId('events-table')).toContainText(`hook.${name}`)
  await page.goto('/webhooks')
  await expect(page.getByTestId('inbound-table').getByRole('row', { name: new RegExp(name) })).toContainText('1')
})

test('outbound hook: create, test send is logged, pause and delete', async ({ page }) => {
  const name = `out-${run}`
  await page.goto('/webhooks')
  await page.getByRole('tab', { name: 'Outbound' }).click()
  await page.getByRole('button', { name: 'Create outbound hook' }).click()
  const dialog = page.getByRole('dialog', { name: 'Create outbound hook' })
  await dialog.getByLabel('Name').fill(name)
  await dialog.getByLabel('Subject filter').fill(`evt.out${run}.>`)
  await dialog.getByLabel('URL').fill('https://pulse-e2e.invalid/hook')
  await dialog.getByRole('button', { name: 'Create' }).click()
  await expect(page.getByTestId('secret-value')).toContainText('whsec_')
  await page.getByRole('button', { name: 'I saved it' }).click()

  await page.getByRole('button', { name: `Test ${name}` }).click()
  await expect(page.getByText(/Test failed/)).toBeVisible()
  await page.getByRole('button', { name: `Deliveries of ${name}` }).click()
  await expect(page.getByTestId('deliveries-table')).toContainText('test')
  await page.keyboard.press('Escape')

  await page.getByRole('button', { name: `Pause ${name}` }).click()
  await expect(page.getByTestId('outbound-table').getByRole('row', { name: new RegExp(name) })).toContainText('paused')
  await page.getByRole('button', { name: `Delete ${name}` }).click()
  const confirm = page.getByRole('dialog', { name: `Delete ${name}` })
  await confirm.getByLabel('confirmation').fill(name)
  await confirm.getByRole('button', { name: 'Delete' }).click()
  await expect(page.getByTestId('outbound-table').getByRole('row', { name: new RegExp(name) })).toHaveCount(0)
})
