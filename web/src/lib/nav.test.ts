import { nav } from './nav'

test('nav paths are unique and absolute', () => {
  const paths = nav.map((n) => n.to)
  expect(new Set(paths).size).toBe(paths.length)
  for (const p of paths) expect(p.startsWith('/')).toBe(true)
})
