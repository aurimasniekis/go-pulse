import { describe, expect, it } from 'vitest'
import { expiryBadge } from './certs'

describe('expiryBadge', () => {
  it.each([
    [{ present: false, days_left: 0 }, 'gray', 'not present'],
    [{ present: true, days_left: -1 }, 'red', 'expired'],
    [{ present: true, days_left: 5 }, 'red', '5d left'],
    [{ present: true, days_left: 20 }, 'yellow', '20d left'],
    [{ present: true, days_left: 80 }, 'teal', '80d left'],
  ])('%o → %s %s', (c, color, label) => {
    expect(expiryBadge(c)).toEqual({ color, label })
  })
})
