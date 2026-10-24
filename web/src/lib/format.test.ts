import { formatBytes, formatDuration, formatNumber, hexDump, payloadBytes, prettyJSON, preview, shellQuote, timeAgo, formatTime, getTimeZone, setTimeZone } from './format'

test('formatBytes', () => {
  expect(formatBytes(0)).toBe('0 B')
  expect(formatBytes(1536)).toBe('1.5 KiB')
  expect(formatBytes(10 * 1024 ** 3)).toBe('10.0 GiB')
  expect(formatBytes(-1)).toBe('∞')
  expect(formatBytes(undefined)).toBe('—')
})

test('formatNumber and durations', () => {
  expect(formatNumber(1234567)).toBe('1,234,567')
  expect(formatDuration(0)).toBe('forever')
  expect(formatDuration(90061)).toBe('1d 1h')
  expect(formatDuration(3600)).toBe('1h')
})

test('timeAgo', () => {
  const now = Date.parse('2026-10-23T12:00:00Z')
  expect(timeAgo('2026-10-23T11:59:30Z', now)).toBe('30s ago')
  expect(timeAgo('2026-10-23T10:00:00Z', now)).toBe('2h ago')
  expect(timeAgo('0001-01-01T00:00:00Z', now)).toBe('—')
})

test('payload helpers', () => {
  expect(prettyJSON('{"a":1}')).toBe('{\n  "a": 1\n}')
  expect(prettyJSON('hello')).toBeUndefined()
  expect(prettyJSON('{broken')).toBeUndefined()
  const bytes = payloadBytes('AAEC/w==', 'base64')
  expect(Array.from(bytes)).toEqual([0, 1, 2, 255])
  expect(hexDump(bytes)).toBe(`00000000  ${'00 01 02 ff'.padEnd(47)}  ....`)
  expect(preview('AAEC/w==', 'base64')).toBe('<4 bytes binary>')
  expect(preview('a\n  b', 'utf8')).toBe('a b')
})

test('shellQuote', () => {
  expect(shellQuote('evt.home.>')).toBe(`'evt.home.>'`)
  expect(shellQuote('evt.home.door')).toBe('evt.home.door')
  expect(shellQuote("it's")).toBe(`'it'\\''s'`)
})

describe('formatTime time zone', () => {
  it('formats in UTC with a Z when chosen, and back to local', () => {
    setTimeZone('utc')
    expect(getTimeZone()).toBe('utc')
    expect(formatTime('2026-10-23T21:04:05.123Z')).toBe('2026-10-23 21:04:05.123Z')
    setTimeZone('local')
    expect(formatTime('2026-10-23T21:04:05.123Z')).not.toContain('Z')
    expect(formatTime(undefined)).toBe('—')
  })
})
