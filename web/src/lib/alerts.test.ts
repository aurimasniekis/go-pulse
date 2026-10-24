import { describe, expect, it } from 'vitest'
import { paramSummary } from './alerts'

describe('paramSummary', () => {
  it('describes built-in and threshold rules', () => {
    expect(paramSummary('stream_usage', { ratio: 0.85 })).toBe('> 85% of max_bytes')
    expect(paramSummary('ban_storm', { count: 10, window_minutes: 10 })).toBe('≥ 10 bans / 10 min')
    expect(paramSummary('threshold', { dimension: 'prefix', key: 'evt.home', op: 'below', threshold: 1, window_minutes: 60, metric: 'msgs' })).toBe(
      'evt.home.> < 1 events / 60 min',
    )
    expect(paramSummary('threshold', { dimension: 'total', op: 'above', threshold: 5, window_minutes: 5, metric: 'bytes' })).toBe('all events > 5 bytes / 5 min')
  })
})
