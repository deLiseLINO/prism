import { describe, expect, it } from 'vitest'
import { barRatio, STATS_RANGES } from '../renderer/src/views/StatsView'

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
