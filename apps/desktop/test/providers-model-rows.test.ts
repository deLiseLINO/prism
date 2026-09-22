import { describe, expect, it } from 'vitest'
import { defaultEffort, rungChips, visibleModelRows } from '../renderer/src/views/ProvidersView'

describe('default effort rung', () => {
  it('prefers medium, then high, then the first rung', () => {
    expect(defaultEffort(['minimal', 'low', 'medium', 'high'])).toBe('medium')
    expect(defaultEffort(['low', 'high'])).toBe('high')
    expect(defaultEffort(['off', 'low'])).toBe('off')
    expect(defaultEffort([])).toBe('')
  })
})

describe('rung chips', () => {
  it('marks the default rung and skips models without a family ladder', () => {
    const efforts = { 'gemini-3.7-flash': ['minimal', 'low', 'medium', 'high'] }
    expect(rungChips('gemini-3.7-flash', efforts)).toEqual([
      { effort: 'minimal', defaultRung: false },
      { effort: 'low', defaultRung: false },
      { effort: 'medium', defaultRung: true },
      { effort: 'high', defaultRung: false },
    ])
    expect(rungChips('claude-opus-4', efforts)).toEqual([])
    expect(rungChips('gemini-3.7-flash', undefined)).toEqual([])
  })
})

describe('model row selection', () => {
  it('lists whatever the daemon stores and filters by the needle', () => {
    expect(visibleModelRows(['claude-opus-4', 'gemini-3.7-flash'], '')).toEqual(['claude-opus-4', 'gemini-3.7-flash'])
    expect(visibleModelRows(['claude-opus-4', 'gemini-3.7-flash'], 'opus')).toEqual(['claude-opus-4'])
    expect(visibleModelRows(['gemini-3.7-flash-low', 'gemini-3.7-flash-high'], 'low')).toEqual(['gemini-3.7-flash-low'])
  })
})
