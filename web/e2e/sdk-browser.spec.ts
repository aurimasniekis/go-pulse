import { existsSync, readFileSync } from 'node:fs'
import { expect, test } from '@playwright/test'

// PL-SDK-005: the TS SDK in a real browser over /ws (the public listener's NATS WebSocket proxy). Go (pulsed's admin
// publish) writes the events; the browser replays them with `since`, sees identical envelopes, and publishes back.
const bundle = '../sdk/ts/dist/pulse.browser.js'
const csrf = { 'X-Pulse-CSRF': '1' }

test('browser SDK replays over /ws and publishes', async ({ page, request }) => {
  test.skip(!existsSync(bundle), 'build the SDK first: make sdk-build')
  const run = Date.now().toString(36)
  const domain = `evt.e2ews${run}`
  const created = await request.post('/api/admin/v1/clients', {
    headers: csrf,
    data: { name: `e2e-browser-${run}`, grants: [{ action: 'subscribe', subject: `${domain}.>` }, { action: 'publish', subject: `${domain}.>` }] },
  })
  expect(created.status()).toBe(201)
  const { creds } = (await created.json()) as { creds: string }
  const published: { id: string; seq: number }[] = []
  for (const [i, type] of ['e2e.ws.a.v1', 'e2e.ws.b.v1', 'e2e.ws.c.v1'].entries()) {
    const r = await request.post('/api/admin/v1/events', { headers: csrf, data: { subject: `${domain}.thing.happened`, type, content_type: 'application/json', data: JSON.stringify({ i }) } })
    expect(r.status()).toBe(201)
    published.push((await r.json()) as { id: string; seq: number })
  }

  const wsURL = (process.env.PULSE_E2E_PUBLIC_URL ?? 'http://127.0.0.1:8443').replace(/^http/, 'ws') + '/ws'
  await page.setContent('<!doctype html><title>sdk</title>')
  await page.addScriptTag({ content: readFileSync(bundle, 'utf8') })
  const out = await page.evaluate(
    async ({ wsURL, creds, domain }) => {
      const P = (globalThis as unknown as { Pulse: typeof import('../../sdk/ts/src/index') }).Pulse
      const c = await P.connect({ servers: wsURL, creds, source: 'browser-e2e' })
      const seen: { id: string; seq: number; type: string; time?: string; data: unknown; transport: string }[] = []
      for await (const e of c.subscribe(`${domain}.>`, { since: '10m' })) {
        seen.push({ id: e.id, seq: e.sequence, type: e.type, time: e.time, data: e.json(), transport: typeof WebSocket })
        if (seen.length === 3) break
      }
      const ack = await c.publish(`${domain}.browser.said`, 'e2e.ws.browser.v1', { from: navigator.userAgent.includes('Chrome') ? 'chrome' : 'browser' })
      await c.close()
      return { seen, ack }
    },
    { wsURL, creds, domain },
  )
  expect(out.seen.map((e) => [e.id, e.seq])).toEqual(published.map((p) => [p.id, p.seq]))
  expect(out.seen.map((e) => e.type)).toEqual(['e2e.ws.a.v1', 'e2e.ws.b.v1', 'e2e.ws.c.v1'])
  expect(out.seen.map((e) => e.data)).toEqual([{ i: 0 }, { i: 1 }, { i: 2 }])
  expect(out.seen[0]?.time).toMatch(/Z$/)

  const back = await request.get(`/api/admin/v1/events?subject=${domain}.browser.said`)
  const page1 = (await back.json()) as { events: { id: string; type: string; source: string }[] }
  expect(page1.events[0]).toMatchObject({ id: out.ack.id, type: 'e2e.ws.browser.v1', source: 'browser-e2e' })
})
