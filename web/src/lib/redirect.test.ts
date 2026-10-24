import { safeNextPath } from './redirect'

test('only same-origin relative paths survive', () => {
  const o = 'https://admin.events.example.com'
  expect(safeNextPath('/explorer?x=1#a', o)).toBe('/explorer?x=1#a')
  for (const bad of ['https://evil.example/', '//evil.example', '/.//evil.example', '/%2e/%2e//x', '\\\\evil', 'explorer', '/login',
    '/api/admin/v1/users', '/auth/oidc/x/start', '/a\nb', '', undefined, 42]) {
    expect(safeNextPath(bad, o)).toBe('/')
  }
})
