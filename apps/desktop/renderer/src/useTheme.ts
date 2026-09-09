import { useCallback, useEffect, useState } from 'react'

export type ThemeMode = 'auto' | 'light' | 'dark'
export type EffectiveTheme = 'light' | 'dark'

const STORAGE_KEY = 'prism-theme'
const MODE_ORDER: readonly ThemeMode[] = ['auto', 'dark', 'light']

export function isThemeMode(value: unknown): value is ThemeMode {
  return value === 'auto' || value === 'light' || value === 'dark'
}

export function resolveTheme(mode: ThemeMode, prefersDark: boolean): EffectiveTheme {
  if (mode === 'auto') return prefersDark ? 'dark' : 'light'
  return mode
}

export function nextThemeMode(mode: ThemeMode): ThemeMode {
  return MODE_ORDER[(MODE_ORDER.indexOf(mode) + 1) % MODE_ORDER.length]!
}

function storedMode(): ThemeMode {
  try {
    const value = window.localStorage.getItem(STORAGE_KEY)
    return isThemeMode(value) ? value : 'auto'
  } catch {
    return 'auto'
  }
}

export function useTheme() {
  const [mode, setMode] = useState<ThemeMode>(storedMode)
  const [prefersDark, setPrefersDark] = useState(() => window.matchMedia('(prefers-color-scheme: dark)').matches)

  useEffect(() => {
    const query = window.matchMedia('(prefers-color-scheme: dark)')
    const onChange = (): void => setPrefersDark(query.matches)
    query.addEventListener('change', onChange)
    return () => query.removeEventListener('change', onChange)
  }, [])

  const effective = resolveTheme(mode, prefersDark)

  useEffect(() => {
    document.documentElement.dataset.theme = effective
    void window.prism.window.setTheme(effective).catch(() => undefined)
  }, [effective])

  useEffect(() => {
    try {
      window.localStorage.setItem(STORAGE_KEY, mode)
    } catch {
      return
    }
  }, [mode])

  const cycle = useCallback((): void => {
    setMode(nextThemeMode)
  }, [])

  return { mode, effective, cycle }
}
