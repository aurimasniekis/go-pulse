import { nav, visibleNav } from './nav'

test('nav paths are unique and absolute', () => {
  const paths = nav.map((n) => n.to)
  expect(new Set(paths).size).toBe(paths.length)
  for (const p of paths) expect(p.startsWith('/')).toBe(true)
})

test('every nav entry has a route', async () => {
  const { routeTree } = await import('@/routeTree.gen')
  const { createRouter } = await import('@tanstack/react-router')
  const { QueryClient } = await import('@tanstack/react-query')
  const router = createRouter({ routeTree, context: { queryClient: new QueryClient() } })
  const paths = new Set(Object.keys(router.routesByPath).map((p) => (p === '/' ? p : p.replace(/\/$/, ''))))
  for (const n of nav) expect(paths, n.to).toContain(n.to)
})

test('every nav entry names a permission and is hidden without it', () => {
  for (const n of nav) expect(n.perm).toMatch(/^[a-z]+\.[a-z]+$/)
  expect(visibleNav([])).toEqual([])
  const viewer = ['dashboard.read', 'events.read', 'clients.read', 'webhooks.read', 'security.read', 'alerts.read', 'backups.read', 'system.read']
  const labels = visibleNav(viewer).map((n) => n.label)
  expect(labels).toContain('Overview')
  expect(labels).not.toContain('Audit')
  expect(labels).not.toContain('Users')
  expect(visibleNav([...viewer, 'audit.read', 'users.read']).map((n) => n.label)).toEqual(expect.arrayContaining(['Audit', 'Users']))
})
