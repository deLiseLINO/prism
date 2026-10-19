import { describe, expect, it } from 'vitest'
import { DEFAULT_PRIMARY_STYLE, parsePrimaryStyle } from '../renderer/src/primaryStyle'

describe('parsePrimaryStyle', () => {
  it('defaults to ink', () => {
    expect(DEFAULT_PRIMARY_STYLE).toBe('ink')
    expect(parsePrimaryStyle(null)).toBe('ink')
    expect(parsePrimaryStyle('nonsense')).toBe('ink')
  })

  it('accepts accent', () => {
    expect(parsePrimaryStyle('accent')).toBe('accent')
    expect(parsePrimaryStyle('ink')).toBe('ink')
  })
})
