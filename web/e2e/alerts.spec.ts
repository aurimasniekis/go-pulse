import { expect, test } from '@playwright/test'

test('alerts: built-in rules, custom dead-man rule, toggle, delete', async ({ page }) => {
  const name = `E2E quiet ${Date.now()}`
  await page.goto('/alerts')
  const rules = page.getByTestId('rules')
  await expect(rules.getByTestId('rule-cert_expiry')).toContainText('Certificate expiry')
  await page.getByRole('button', { name: 'Evaluate now' }).click()
  await expect(page.getByText(/Evaluated: \d+ firing/)).toBeVisible()
  // The dev stack backs up to SeaweedFS, so the backup rule has data and is healthy.
  await expect(rules.getByTestId('rule-backup_stale')).toContainText(/ok$/)

  // A dead-man switch on a prefix nobody publishes to fires on the next evaluation.
  await page.getByRole('button', { name: 'New rule' }).click()
  const dialog = page.getByRole('dialog', { name: 'New threshold rule' })
  await dialog.getByLabel('Name').fill(name)
  await dialog.getByLabel('Subject prefix').fill('evt.e2enobody')
  await dialog.getByLabel('Window (minutes)').fill('5')
  await dialog.getByRole('button', { name: 'Create' }).click()
  await expect(page.getByText(`Rule ${name} created`)).toBeVisible()
  const row = rules.getByRole('row', { name: new RegExp(name) })
  await expect(row).toContainText('evt.e2enobody.> < 1 events / 5 min')

  await page.getByRole('button', { name: 'Evaluate now' }).click()
  await expect(page.getByTestId('firing')).toContainText(name)
  await expect(row).toContainText('1 firing')
  await expect(page.getByTestId('alert-history')).toContainText(name)

  // Disabling resolves it; deleting removes it.
  await row.getByRole('switch', { name: `Enable ${name}` }).click({ force: true })
  await expect(page.getByText(`${name} disabled`)).toBeVisible()
  await expect(row).toContainText('disabled')
  await row.getByRole('button', { name: `Delete ${name}` }).click()
  const confirm = page.getByRole('dialog', { name: `Delete rule ${name}` })
  await confirm.getByLabel('confirmation').fill(name)
  await confirm.getByRole('button', { name: 'Delete' }).click()
  await expect(page.getByText('Rule deleted')).toBeVisible()
  await expect(rules.getByRole('row', { name: new RegExp(name) })).toHaveCount(0)
})
