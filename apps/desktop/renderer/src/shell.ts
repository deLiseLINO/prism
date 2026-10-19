import type { View } from './routing'

export const SIDEBAR_KEY = 'prism-sidebar'

export function viewForShortcut(key: string, visible: readonly View[]): View | null {
  if (!/^[1-9]$/.test(key)) return null
  return visible[Number(key) - 1] ?? null
}

// The rail is thin by default; only an explicit stored choice expands it.
export function initialSidebarOpen(stored: string | null): boolean {
  return stored === 'open'
}

export interface NavigationLike {
  readonly canGoBack: boolean
  readonly canGoForward: boolean
}

export interface HistoryAvailability {
  readonly back: boolean
  readonly forward: boolean
}

// Without the Navigation API history depth is unknowable, so both stay enabled.
export function historyAvailability(navigation: NavigationLike | undefined): HistoryAvailability {
  if (navigation === undefined) return { back: true, forward: true }
  return { back: navigation.canGoBack, forward: navigation.canGoForward }
}
