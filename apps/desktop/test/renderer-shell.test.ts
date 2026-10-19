import { describe, expect, it } from 'vitest'
import { historyAvailability, initialSidebarOpen, viewForShortcut } from '../renderer/src/shell'

describe('viewForShortcut', () => {
  const visible = ['overview', 'usage', 'logs'] as const
  it('maps digits to the Nth visible view', () => {
    expect(viewForShortcut('1', visible)).toBe('overview')
    expect(viewForShortcut('3', visible)).toBe('logs')
  })
  it('ignores digits past the list and non-digits', () => {
    expect(viewForShortcut('4', visible)).toBeNull()
    expect(viewForShortcut('0', visible)).toBeNull()
    expect(viewForShortcut('b', visible)).toBeNull()
  })
})

describe('initialSidebarOpen', () => {
  it('is thin unless the expanded state was stored', () => {
    expect(initialSidebarOpen('open')).toBe(true)
    expect(initialSidebarOpen('closed')).toBe(false)
    expect(initialSidebarOpen(null)).toBe(false)
  })
})

describe('historyAvailability', () => {
  it('reads the Navigation API when present', () => {
    expect(historyAvailability({ canGoBack: false, canGoForward: true })).toEqual({ back: false, forward: true })
  })
  it('stays enabled when the API is missing', () => {
    expect(historyAvailability(undefined)).toEqual({ back: true, forward: true })
  })
})
