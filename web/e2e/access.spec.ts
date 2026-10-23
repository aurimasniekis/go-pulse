import { expect, test } from '@playwright/test'

const run = Date.now().toString(36)
const publicURL = process.env.PULSE_E2E_PUBLIC_URL ?? 'http://127.0.0.1:8443'

test('clients: create, show creds once, edit grants, rotate, revoke', async ({ page }) => {
  const name = `e2e-${run}`
  await page.goto('/clients')
  await page.getByRole('button', { name: 'Create client' }).click()
  const dialog = page.getByRole('dialog', { name: 'Create client' })
  await dialog.getByLabel('Name').fill(name)
  await dialog.getByRole('button', { name: 'Add grant' }).click()
  await dialog.getByLabel('Grant 1 subject').fill(`evt.e2e${run}.>`)
  await dialog.getByRole('button', { name: 'Create and issue credentials' }).click()

  const creds = page.getByRole('dialog', { name: `Credentials for ${name}` })
  await expect(creds.getByTestId('secret-value')).toContainText('BEGIN NATS USER JWT')
  await expect(creds.getByTestId('snippets')).toContainText('pulse.Connect(')
  await creds.getByRole('button', { name: 'I saved it' }).click()
  await expect(page.getByTestId('secret-value')).toHaveCount(0)

  await page.getByTestId('clients-table').getByRole('link', { name }).click()
  await expect(page.getByRole('heading', { name })).toBeVisible()
  await page.getByRole('button', { name: 'Add grant' }).click()
  await page.getByRole('textbox', { name: 'Grant 2 action' }).click()
  await page.getByRole('option', { name: 'subscribe' }).click()
  await page.getByLabel('Grant 2 subject').fill(`evt.e2e${run}.>`)
  await page.getByRole('button', { name: 'Save grants' }).click()
  await expect(page.getByText(/Grants saved/)).toBeVisible()

  await page.getByRole('button', { name: 'Rotate credentials' }).click()
  await page.getByRole('dialog', { name: 'Rotate credentials' }).getByRole('button', { name: 'Rotate' }).click()
  await expect(page.getByTestId('secret-value')).toContainText('USER NKEY SEED')
  await page.getByRole('button', { name: 'I saved it' }).click()

  await page.getByRole('button', { name: 'Revoke' }).click()
  const confirm = page.getByRole('dialog', { name: `Revoke ${name}` })
  await confirm.getByLabel('confirmation').fill(name)
  await confirm.getByRole('button', { name: 'Revoke' }).click()
  await expect(page.getByText('revoked', { exact: true }).first()).toBeVisible()

  await page.goto(`/audit?action=client.&target=`)
  await expect(page.getByTestId('audit-table')).toContainText('client.revoke')
})

test('tokens: create, publish through the public API, revoke', async ({ page, request }) => {
  const name = `tok-${run}`
  await page.goto('/tokens')
  await page.getByRole('button', { name: 'Create token' }).click()
  const dialog = page.getByRole('dialog', { name: 'Create HTTP token' })
  await dialog.getByLabel('Name').fill(name)
  await dialog.getByLabel('Grant 1 subject').fill(`evt.tok${run}.>`)
  await dialog.getByRole('button', { name: 'Create' }).click()
  const secret = (await page.getByTestId('secret-value').textContent())?.trim() ?? ''
  expect(secret).toMatch(/^pulse_[a-z0-9]{8}_/)
  await page.getByRole('button', { name: 'I saved it' }).click()

  const ok = await request.post(`${publicURL}/api/v1/publish/evt.tok${run}.build.done`, {
    headers: { Authorization: `Bearer ${secret}`, 'Ce-Type': 'ci.build.done.v1', 'Content-Type': 'application/json' },
    data: { status: 'green' },
  })
  expect(ok.status()).toBe(201)
  const denied = await request.post(`${publicURL}/api/v1/publish/evt.other${run}.build.done`, { headers: { Authorization: `Bearer ${secret}` }, data: 'x' })
  expect(denied.status()).toBe(403)

  await page.goto(`/explorer?subject=evt.tok${run}.>`)
  await expect(page.getByTestId('events-table').getByRole('row')).toHaveCount(1)
  await expect(page.getByTestId('events-table')).toContainText('ci.build.done.v1')

  await page.goto('/tokens')
  await page.getByRole('button', { name: `Revoke ${name}` }).click()
  const confirm = page.getByRole('dialog', { name: `Revoke ${name}` })
  await confirm.getByLabel('confirmation').fill(name)
  await confirm.getByRole('button', { name: 'Revoke' }).click()
  await expect(page.getByTestId('tokens-table').getByRole('row', { name: new RegExp(name) })).toContainText('revoked')
  const after = await request.post(`${publicURL}/api/v1/publish/evt.tok${run}.build.done`, { headers: { Authorization: `Bearer ${secret}` }, data: 'x' })
  expect(after.status()).toBe(401)
})
