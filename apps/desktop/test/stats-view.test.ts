import { describe, expect, it } from 'vitest'
import { barRatio, isStatsRange, STATS_RANGES, storedRange } from '../renderer/src/views/StatsView'

describe('stats view helpers', () => {
  it('normalizes bar ratios against the row maximum', () => {
    expect(barRatio(500, 1000)).toBe(0.5)
    expect(barRatio(1000, 1000)).toBe(1)
    expect(barRatio(1500, 1000)).toBe(1)
    expect(barRatio(0, 1000)).toBe(0)
    expect(barRatio(300, 0)).toBe(0)
  })

  it('offers all five daemon-supported ranges with the default among them', () => {
    expect(STATS_RANGES.map((option) => option.value)).toEqual([
      '1h',
      '24h',
      '7d',
      '30d',
      'all',
    ])
    expect(STATS_RANGES[1]).toEqual({ value: '24h', label: 'Last 24 hours' })
  })
})

describe('stats range persistence', () => {
  it('accepts the five daemon ranges and rejects anything else', () => {
    for (const value of ['1h', '24h', '7d', '30d', 'all']) {
      expect(isStatsRange(value)).toBe(true)
    }
    expect(isStatsRange('bogus')).toBe(false)
    expect(isStatsRange(null)).toBe(false)
    expect(isStatsRange(undefined)).toBe(false)
  })

  it('restores the last picked range and falls back to 24h', () => {
    const store = new Map<string, string>()
    Object.defineProperty(globalThis, 'window', {
      configurable: true,
      value: {
        localStorage: {
          getItem: (key: string) => store.get(key) ?? null,
          setItem: (key: string, value: string) => void store.set(key, value),
        },
      },
      writable: true,
    })

    store.set('prism-stats-range', '30d')
    expect(storedRange()).toBe('30d')

    store.set('prism-stats-range', 'bogus')
    expect(storedRange()).toBe('24h')

    store.clear()
    expect(storedRange()).toBe('24h')
  })
})
