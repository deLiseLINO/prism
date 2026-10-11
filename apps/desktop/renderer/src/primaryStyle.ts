import { useCallback, useEffect, useState } from 'react'

export type PrimaryStyle = 'ink' | 'accent'

export const PRIMARY_STYLE_KEY = 'prism-primary'
export const DEFAULT_PRIMARY_STYLE: PrimaryStyle = 'ink'

export function parsePrimaryStyle(raw: string | null): PrimaryStyle {
  return raw === 'accent' ? 'accent' : DEFAULT_PRIMARY_STYLE
}

function readStored(): PrimaryStyle {
  try {
    return parsePrimaryStyle(window.localStorage.getItem(PRIMARY_STYLE_KEY))
  } catch {
    return DEFAULT_PRIMARY_STYLE
  }
}

/* html[data-primary] drives --primary-bg/--primary-fg in styles.css; theme-boot.js applies the
   stored value before first paint. */
export function usePrimaryStyle(): readonly [PrimaryStyle, (next: PrimaryStyle) => void] {
  const [style, setStyleState] = useState<PrimaryStyle>(readStored)

  useEffect(() => {
    document.documentElement.dataset.primary = style
  }, [style])

  const setStyle = useCallback((next: PrimaryStyle) => {
    setStyleState(next)
    try {
      window.localStorage.setItem(PRIMARY_STYLE_KEY, next)
    } catch {
      return
    }
  }, [])

  return [style, setStyle]
}
