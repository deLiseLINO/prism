import { useCallback, useEffect, useState } from 'react'

export type Skin = 'obsidian' | 'graphite'

const STORAGE_KEY = 'prism-skin'
const SKIN_ORDER: readonly Skin[] = ['obsidian', 'graphite']

export function isSkin(value: unknown): value is Skin {
  return value === 'obsidian' || value === 'graphite'
}

export function nextSkin(skin: Skin): Skin {
  return SKIN_ORDER[(SKIN_ORDER.indexOf(skin) + 1) % SKIN_ORDER.length]!
}

function storedSkin(): Skin {
  try {
    const value = window.localStorage.getItem(STORAGE_KEY)
    return isSkin(value) ? value : 'obsidian'
  } catch {
    return 'obsidian'
  }
}

export function useSkin() {
  const [skin, setSkin] = useState<Skin>(storedSkin)

  useEffect(() => {
    document.documentElement.dataset.skin = skin
  }, [skin])

  useEffect(() => {
    try {
      window.localStorage.setItem(STORAGE_KEY, skin)
    } catch {
      return
    }
  }, [skin])

  const cycle = useCallback((): void => {
    setSkin(nextSkin)
  }, [])

  return { skin, cycle }
}
