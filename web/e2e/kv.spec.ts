import { expect, test } from '@playwright/test'

const csrf = { 'X-Pulse-CSRF': '1' }

test('kv: create bucket, set and read a key, history, delete', async ({ page, request }) => {
  const bucket = `e2e_kv_${Date.now().toString(36)}`
  await page.goto('/kv')
  await page.getByRole('button', { name: 'New KV bucket' }).click()
  const create = page.getByRole('dialog', { name: 'Create KV bucket' })
  await create.getByLabel('Name').fill(bucket)
  await create.getByLabel('History per key').fill('3')
  await create.getByRole('button', { name: 'Create' }).click()
  await expect(page.getByText(`Bucket ${bucket} created`)).toBeVisible()

  await page.goto(`/kv?bucket=${bucket}`)
  for (const v of ['{"mode":"night"}', '{"mode":"day"}']) {
    await request.put(`/api/admin/v1/kv/${bucket}/entry?key=ui.mode`, { headers: csrf, data: { value: v, encoding: 'utf8' } })
  }
  await page.getByRole('button', { name: 'New key' }).click()
  const put = page.getByRole('dialog', { name: `New key in ${bucket}` })
  await put.getByLabel('Key').fill('devices.lamp')
  await put.getByLabel('Value').fill('on')
  await put.getByRole('button', { name: 'Save' }).click()
  await expect(page.getByText(/devices.lamp set \(revision \d+\)/)).toBeVisible()

  const keys = page.getByTestId('kv-keys')
  await expect(keys.getByRole('row')).toHaveCount(2)
  await keys.getByRole('row', { name: /ui\.mode/ }).click()
  const drawer = page.getByRole('dialog', { name: 'ui.mode' })
  await expect(drawer.getByTestId('payload')).toContainText('"mode": "day"')
  await expect(drawer.getByRole('row')).toHaveCount(2) // two revisions
  await drawer.getByRole('button', { name: 'Delete' }).click()
  await expect(page.getByText('Key deleted')).toBeVisible()
  await expect(keys.getByRole('row')).toHaveCount(1)

  await page.getByRole('button', { name: 'Delete bucket' }).click()
  const confirm = page.getByRole('dialog', { name: `Delete bucket ${bucket}` })
  await confirm.getByLabel('confirmation').fill(bucket)
  await confirm.getByRole('button', { name: 'Delete' }).click()
  await expect(page.getByText(`Bucket ${bucket} deleted`)).toBeVisible()
})

test('objects: upload, download, delete; blobs is read-only', async ({ page, request }) => {
  const bucket = `e2e_obj_${Date.now().toString(36)}`
  await page.goto('/kv?tab=objects')
  await page.getByRole('button', { name: 'New object store' }).click()
  const create = page.getByRole('dialog', { name: 'Create object store' })
  await create.getByLabel('Name').fill(bucket)
  await create.getByRole('button', { name: 'Create' }).click()
  await expect(page.getByText(`Bucket ${bucket} created`)).toBeVisible()
  await page.goto(`/kv?tab=objects&bucket=${bucket}`)
  const chooser = page.waitForEvent('filechooser')
  await page.getByRole('button', { name: 'Upload' }).click()
  await (await chooser).setFiles({ name: 'notes.txt', mimeType: 'text/plain', buffer: Buffer.from('hello from e2e') })
  await expect(page.getByText('Uploaded')).toBeVisible()
  const row = page.getByTestId('objects').getByRole('row', { name: /notes\.txt/ })
  await expect(row).toContainText('14 B')
  const download = page.waitForEvent('download')
  await row.getByRole('link', { name: 'Download notes.txt' }).click()
  expect((await download).suggestedFilename()).toBe('notes.txt')
  await row.getByRole('button', { name: 'Delete notes.txt' }).click()
  const confirm = page.getByRole('dialog', { name: 'Delete notes.txt' })
  await confirm.getByLabel('confirmation').fill('notes.txt')
  await confirm.getByRole('button', { name: 'Delete' }).click()
  await expect(page.getByText('Object deleted')).toBeVisible()
  await request.delete(`/api/admin/v1/objects/${bucket}?confirm=${bucket}`, { headers: csrf })

  await page.goto('/kv?tab=objects&bucket=blobs')
  await expect(page.getByText('Managed by pulsed')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Upload' })).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Delete bucket' })).toHaveCount(0)
})

test('audit: time filter and settings time zone', async ({ page }) => {
  await page.goto('/audit')
  await page.getByRole('textbox', { name: 'Time' }).click()
  await page.getByRole('option', { name: 'Last hour' }).click()
  await page.getByRole('button', { name: 'Apply' }).click()
  await expect(page).toHaveURL(/since=/)
  await expect(page.getByTestId('audit-table')).toBeVisible()

  await page.goto('/settings')
  await expect(page.getByTestId('config')).toContainText('PULSE_BACKUP_ENABLED')
  await expect(page.getByTestId('config')).not.toContainText('pulse-dev-not-a-secret')
  await page.getByTestId('tz').getByText('UTC').click()
  await expect(page.getByTestId('tz-example')).toContainText(/\d{2}:\d{2}:\d{2}\.\d{3}Z/)
  await page.reload()
  await expect(page.getByTestId('tz-example')).toContainText('Z.')
  await page.getByTestId('tz').getByText('Local').click()
})
