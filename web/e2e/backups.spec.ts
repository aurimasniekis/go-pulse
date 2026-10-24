import { readFileSync } from 'node:fs'
import { expect, test } from '@playwright/test'
import { csrf } from './admin'


test('backups: back up now, then restore a deleted stream with the dev identity', async ({ page, request }) => {
  test.setTimeout(120_000)
  const name = `E2E_BK_${Date.now().toString(36).toUpperCase()}`
  const created = await request.post('/api/admin/v1/streams', {
    headers: csrf,
    data: { name, subjects: [`e2ebk.${name.toLowerCase()}.>`], storage: 'file', max_bytes: -1, max_age_seconds: 0, max_msgs: -1, discard: 'old', compression: true, duplicates_seconds: 120, allow_direct: true },
  })
  expect(created.status()).toBe(201)

  await page.goto('/backups')
  await expect(page.getByText('Daily at')).toBeVisible()
  await page.getByRole('button', { name: 'Back up now' }).click()
  await expect(page.getByText('Backup started')).toBeVisible()
  const newest = page.getByTestId('backups-table').getByRole('row').nth(1)
  await expect(newest).toContainText('ok', { timeout: 60_000 })
  await expect(newest).toContainText('verified')

  const del = await request.delete(`/api/admin/v1/streams/${name}?confirm=${name}`, { headers: csrf })
  expect(del.status()).toBe(204)

  await newest.getByRole('button', { name: /Restore/ }).click()
  const dialog = page.getByRole('dialog', { name: /Restore from/ })
  await dialog.getByTestId('identity').fill(readFileSync('../deploy/.dev/age.key', 'utf8'))
  await dialog.getByRole('button', { name: 'Show contents' }).click()
  const plan = dialog.getByTestId('restore-plan')
  await expect(plan.getByRole('row', { name: new RegExp(name) })).toContainText('missing')
  await expect(plan.getByRole('row', { name: /EVENTS/ })).toContainText('exists')
  await dialog.getByRole('button', { name: 'Restore 1 stream' }).click()
  await expect(page.getByText('Restore started')).toBeVisible()
  await expect(page.getByTestId('last-restore')).toContainText(`${name}: restored`, { timeout: 30_000 })
  await expect(page.getByTestId('last-restore')).not.toContainText('EVENTS') // only the selected stream is touched
  const info = await request.get(`/api/admin/v1/streams/${name}`)
  expect(info.status()).toBe(200)
  await request.delete(`/api/admin/v1/streams/${name}?confirm=${name}`, { headers: csrf })
})
