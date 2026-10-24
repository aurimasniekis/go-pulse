import { nav } from './nav'

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
