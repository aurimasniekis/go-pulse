import { clientSnippets, exampleSubject, tokenSnippets } from './snippets'

test('exampleSubject fills wildcards to a valid 4-token subject', () => {
  expect(exampleSubject('evt.home.>')).toBe('evt.home.thing.happened')
  expect(exampleSubject('evt.*.door.opened')).toBe('evt.thing.door.opened')
  expect(exampleSubject('evt.a.b.c')).toBe('evt.a.b.c')
})

test('snippets use the client grants and URLs', () => {
  const s = clientSnippets({ name: 'bot', natsUrl: 'tls://r.example:4222', publicUrl: '', grants: [{ action: 'publish', subject: 'evt.home.>' }, { action: 'subscribe', subject: 'evt.work.>' }] })
  expect(s.Go).toContain('pulse.Connect("tls://r.example:4222", pulse.WithCredsFile("bot.creds")')
  expect(s.Go).toContain('"evt.home.thing.happened"')
  expect(s['pulse CLI']).toContain("pulse sub 'evt.work.>' --since 24h")
  const t = tokenSnippets('https://events.example', [{ action: 'publish', subject: 'evt.ci.build.done' }])
  expect(t.Publish).toContain('https://events.example/api/v1/publish/evt.ci.build.done')
})

test('client snippets show only what the grants allow', () => {
  const pubOnly = clientSnippets({ name: 'sensor', natsUrl: 'tls://r.example:4222', publicUrl: '', grants: [{ action: 'publish', subject: 'evt.home.garage.>' }] })
  expect(pubOnly.Go).toContain('c.Publish(ctx, "evt.home.garage.thing"')
  for (const code of Object.values(pubOnly)) {
    expect(code).not.toContain('ubscribe')
    expect(code).not.toContain('pulse sub')
  }

  const subOnly = clientSnippets({ name: 'reader', natsUrl: 'tls://r.example:4222', publicUrl: '', grants: [{ action: 'subscribe', subject: 'evt.github.>' }] })
  expect(subOnly.Go).toContain('c.Subscribe(ctx, "evt.github.>"')
  expect(subOnly.Go).not.toContain('Publish')
  expect(subOnly['pulse CLI']).not.toContain('pulse pub')
  expect(subOnly['nats CLI']).toBeUndefined()

  const durable = clientSnippets({ name: 'agent', natsUrl: 'tls://r.example:4222', publicUrl: '', grants: [{ action: 'durable', name: 'budget', subject: 'evt.finance.>' }] })
  expect(durable.Go).toContain('pulse.Durable("budget")')
  expect(durable.Python).toContain('durable="budget"')
  expect(durable.TypeScript).toContain("durable: 'budget'")
  expect(durable['pulse CLI']).toContain("pulse sub 'evt.finance.>' --durable budget")

  const none = clientSnippets({ name: 'new', natsUrl: 'tls://r.example:4222', publicUrl: '', grants: [] })
  expect(none.Go).toContain('no publish or subscribe grants yet')
  expect(none.Go).not.toContain('evt.example')
})

test('token snippets show only what the grants allow', () => {
  expect(Object.keys(tokenSnippets('', [{ action: 'publish', subject: 'evt.ci.>' }]))).toEqual(['Publish'])
  expect(Object.keys(tokenSnippets('', [{ action: 'subscribe', subject: 'evt.ci.>' }]))).toEqual(['Replay', 'Tail'])
  expect(tokenSnippets('', [])).toEqual({})
})
