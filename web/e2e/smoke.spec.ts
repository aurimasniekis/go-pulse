import { expect, test } from '@playwright/test'

test('shell: status, theme toggle, command palette', async ({ page }) => {
  await page.goto('/')
  await expect(page.getByTestId('status-indicator')).toContainText('NATS')
  const html = page.locator('html')
  const before = await html.getAttribute('data-mantine-color-scheme')
  await page.getByRole('button', { name: 'Toggle color scheme' }).click()
  await expect(html).not.toHaveAttribute('data-mantine-color-scheme', before ?? '')
  await page.keyboard.press('ControlOrMeta+k')
  await page.getByPlaceholder('Jump to…').fill('stream')
  await page.keyboard.press('Enter')
  await expect(page).toHaveURL(/\/streams$/)
})

test('streams: EVENTS is managed; create, inspect and delete a user stream', async ({ page }) => {
  const name = `E2E_${Date.now()}`
  await page.goto('/streams')
  const table = page.getByTestId('streams-table')
  await expect(table.getByRole('row', { name: /EVENTS/ })).toContainText('managed')

  await page.getByRole('button', { name: 'Create stream' }).click()
  const dialog = page.getByRole('dialog', { name: 'Create stream' })
  await dialog.getByLabel('Name').fill(name)
  const subjects = dialog.getByRole('textbox', { name: 'Subjects' })
  await subjects.fill('e2e.orders.>')
  await subjects.press('Enter')
  await dialog.getByRole('button', { name: 'Create' }).click()
  await expect(page.getByText(`Stream ${name} created`)).toBeVisible()

  await table.getByRole('link', { name }).click()
  await expect(page.getByRole('heading', { name })).toBeVisible()
  await expect(page.getByTestId('consumers-table')).toContainText('No consumers')

  await page.getByRole('button', { name: 'Delete' }).click()
  const confirm = page.getByRole('dialog', { name: `Delete stream ${name}` })
  const del = confirm.getByRole('button', { name: 'Delete' })
  await expect(del).toBeDisabled()
  await confirm.getByLabel('confirmation').fill(name)
  await del.click()
  await expect(page).toHaveURL(/\/streams$/)
  await expect(table.getByRole('link', { name })).toHaveCount(0)
})

test('streams: overlapping subjects are rejected', async ({ page }) => {
  await page.goto('/streams')
  await page.getByRole('button', { name: 'Create stream' }).click()
  const dialog = page.getByRole('dialog', { name: 'Create stream' })
  await dialog.getByLabel('Name').fill('E2E_BAD')
  const subjects = dialog.getByRole('textbox', { name: 'Subjects' })
  await subjects.fill('evt.custom.>')
  await subjects.press('Enter')
  await dialog.getByRole('button', { name: 'Create' }).click()
  await expect(page.getByText(/overlaps reserved/)).toBeVisible()
})

test('explorer: renders and keeps filters in the URL', async ({ page }) => {
  await page.goto('/explorer')
  await expect(page.getByTestId('events-table')).toBeVisible()
  await expect(page.getByTestId('events-summary')).toContainText('events')
  await page.getByLabel('Type').fill('(untyped)')
  await page.getByRole('button', { name: 'Apply' }).click()
  await expect(page).toHaveURL(/type=%28untyped%29|type=\(untyped\)/)
})

test('live tail: starts and stops', async ({ page }) => {
  await page.goto('/tail')
  await page.getByRole('button', { name: 'Start' }).click()
  await expect(page.getByTestId('tail-status')).toHaveText('live')
  await page.getByRole('button', { name: 'Stop' }).click()
  await expect(page.getByTestId('tail-status')).toHaveText('idle')
})

test('placeholder pages render', async ({ page }) => {
  for (const path of ['/security', '/system', '/settings']) {
    await page.goto(path)
    await expect(page.getByText('Not built yet')).toBeVisible()
  }
})
