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
