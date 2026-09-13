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

describe('model row selection for the raw toggle', () => {
  const models = ['claude-opus-4', 'gemini-3.7-flash']
  const rawModels = [
    'claude-opus-4-5-thinking',
    'gemini-3.7-flash-high',
    'gemini-3.7-flash-low',
    'gemini-3.7-flash-medium',
    'gemini-3.7-flash-tiered',
  ]

  it('lists logical ids until the toggle flips to the raw wire ids', () => {
    expect(visibleModelRows(models, rawModels, '', false)).toEqual(models)
    expect(visibleModelRows(models, rawModels, '', true)).toEqual(rawModels)
  })

  it('filters raw rows by the needle and survives providers without families', () => {
    expect(visibleModelRows(models, rawModels, 'low', true)).toEqual(['gemini-3.7-flash-low'])
    expect(visibleModelRows(models, rawModels, 'opus', false)).toEqual(['claude-opus-4'])
    expect(visibleModelRows(models, undefined, '', true)).toEqual([])
  })
})
