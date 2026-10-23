import { parseDuration, parseEventsSearch, parseSize } from './search'

test('parseEventsSearch keeps known, non-empty values', () => {
  expect(parseEventsSearch({ stream: 'EVENTS', subject: 'evt.a.>', dir: 'forward', start_seq: '12', junk: 1, type: '' })).toEqual({
    stream: 'EVENTS',
    subject: ['evt.a.>'],
    dir: 'forward',
    start_seq: 12,
  })
  expect(parseEventsSearch({ subject: ['a', '', 'b'], header: 'x=1', dir: 'sideways', start_seq: '-3' })).toEqual({
    subject: ['a', 'b'],
    header: ['x=1'],
  })
})

test('parseSize', () => {
  expect(parseSize('10GiB')).toBe(10 * 1024 ** 3)
  expect(parseSize('500 MB')).toBe(500e6)
  expect(parseSize('1048576')).toBe(1048576)
  expect(parseSize('')).toBeUndefined()
  expect(parseSize('unlimited')).toBe(-1)
  expect(() => parseSize('lots')).toThrow()
})

test('parseDuration', () => {
  expect(parseDuration('30d')).toBe(30 * 86400)
  expect(parseDuration('90')).toBe(90)
  expect(parseDuration('forever')).toBe(0)
  expect(() => parseDuration('soon')).toThrow()
})
