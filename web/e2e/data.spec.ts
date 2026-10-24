import { expect, test, type APIRequestContext } from '@playwright/test'
import { csrf } from './admin'

const run = Date.now().toString(36)

async function seed(request: APIRequestContext) {
  // The dev stack keeps data between runs, so earlier runs' domains fill the 24 h top-10. Give this run's domain a
  // clear lead over the current leader (typed, so the untyped filter below still sees one event).
  const ov = await request.get('/api/admin/v1/overview')
  const top = ((await ov.json()) as { top_prefixes_24h?: { msgs: number }[] }).top_prefixes_24h ?? []
  const lead = Math.max(20, (top[0]?.msgs ?? 0) + 5)
  const events = [
    ...Array.from({ length: 5 }, (_, i) => ({ subject: `evt.e2e${run}.door.opened`, type: 'e2e.door.opened.v1', data: JSON.stringify({ door: 'front', n: i }) })),
    ...Array.from({ length: 3 }, () => ({ subject: `evt.e2e${run}.light.on`, type: 'e2e.light.on.v1', data: '{"room":"hall"}' })),
    { subject: `evt.e2e${run}.raw.thing`, data: 'plain text', content_type: 'text/plain; charset=utf-8' },
    ...Array.from({ length: lead }, (_, i) => ({ subject: `evt.e2e${run}.burst.tick`, type: 'e2e.burst.tick.v1', data: JSON.stringify({ i }) })),
  ]
  for (const e of events) {
    const r = await request.post('/api/admin/v1/events', { headers: csrf, data: { content_type: 'application/json', ...e } })
    expect(r.status()).toBe(201)
  }
}

test.beforeAll(async ({ request }) => {
  await seed(request)
})

test('explorer: filter by subject, open an event, see the payload', async ({ page }) => {
  await page.goto(`/explorer?subject=evt.e2e${run}.door.>`)
  const table = page.getByTestId('events-table')
  await expect(table.getByRole('row')).toHaveCount(5)
  await table.getByRole('row').first().click()
  const drawer = page.getByRole('dialog')
  await expect(drawer).toContainText('e2e.door.opened.v1')
  await expect(drawer.getByTestId('payload')).toContainText('"door": "front"')
  await expect(drawer).toContainText(`pulse sub evt.e2e${run}.door.opened --seq`)
})

test('explorer: untyped filter and publish from the UI', async ({ page }) => {
  await page.goto(`/explorer?subject=evt.e2e${run}.>&type=(untyped)`)
  await expect(page.getByTestId('events-table').getByRole('row')).toHaveCount(1)
  await page.getByRole('button', { name: 'Publish event' }).click()
  const dialog = page.getByRole('dialog', { name: 'Publish event' })
  await dialog.getByLabel('Subject').fill(`evt.e2e${run}.ui.clicked`)
  await dialog.getByLabel('Type', { exact: true }).fill('e2e.ui.clicked.v1')
  await dialog.getByRole('button', { name: 'Publish' }).click()
  await expect(page.getByText(/Published seq \d+/)).toBeVisible()
})

test('overview and subjects reflect the seeded events', async ({ page }) => {
  await page.goto('/')
  await expect(page.getByTestId('kpi-24h')).not.toContainText(/^Last 24 hours0$/, { timeout: 20_000 })
  await expect(page.getByTestId('throughput-chart').locator('svg')).toBeVisible()
  await expect(page.getByTestId('top-domains')).toContainText(`evt.e2e${run}`, { timeout: 20_000 })

  await page.goto('/subjects')
  const tree = page.getByTestId('subject-tree')
  await expect(tree.getByTestId(`node-evt.e2e${run}`)).toBeVisible({ timeout: 20_000 })
  await page.getByRole('button', { name: `Expand evt.e2e${run}` }).click()
  await page.getByRole('button', { name: `Expand evt.e2e${run}.door` }).click()
  await tree.getByText('opened', { exact: true }).click()
  await expect(page.getByRole('dialog')).toContainText('e2e.door.opened.v1')
})
