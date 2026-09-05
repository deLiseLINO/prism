import { describe, expect, it } from 'vitest'
import { isThemeMode, nextThemeMode, resolveTheme } from '../renderer/src/useTheme'

describe('resolveTheme', () => {
  it('follows the system preference in auto mode', () => {
    expect(resolveTheme('auto', true)).toBe('dark')
    expect(resolveTheme('auto', false)).toBe('light')
  })

  it('ignores the system preference once a mode is picked', () => {
    expect(resolveTheme('dark', false)).toBe('dark')
    expect(resolveTheme('light', true)).toBe('light')
  })
})

describe('nextThemeMode', () => {
  it('cycles auto to dark to light and back', () => {
    expect(nextThemeMode('auto')).toBe('dark')
    expect(nextThemeMode('dark')).toBe('light')
    expect(nextThemeMode('light')).toBe('auto')
  })
})

describe('isThemeMode', () => {
  it('accepts the three mode names and rejects everything else', () => {
    expect(isThemeMode('auto')).toBe(true)
    expect(isThemeMode('dark')).toBe(true)
    expect(isThemeMode('light')).toBe(true)
    expect(isThemeMode('system')).toBe(false)
    expect(isThemeMode(null)).toBe(false)
    expect(isThemeMode(undefined)).toBe(false)
  })
})
