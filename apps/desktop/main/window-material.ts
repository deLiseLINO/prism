import { DESKTOP_WINDOW_BLUR_RADIUS_MAX, DESKTOP_WINDOW_BLUR_RADIUS_MIN, type DesktopWindowMaterial } from '@prism/contracts'
import { app, type BrowserWindow } from 'electron'
import path from 'node:path'

export const MAC_WINDOW_VIBRANCY = 'under-window'
export const WINDOW_MATERIAL_ADDON_FILE = 'window-material.node'

export interface WindowMaterialAddon {
  setBackgroundBlurRadius: (nativeWindowHandle: Buffer, radius: number) => boolean
}

type MaterialWindow = Pick<BrowserWindow, 'getNativeWindowHandle' | 'setVibrancy'>

export function parseWindowMaterialRequest(input: unknown): DesktopWindowMaterial {
  if (typeof input !== 'object' || input === null) {
    throw new Error('prism: window material request must be an object')
  }
  const { material, blurRadius } = input as Record<string, unknown>
  if (material !== 'opaque' && material !== 'translucent') {
    throw new Error('prism: window material must be opaque or translucent')
  }
  if (typeof blurRadius !== 'number' || !Number.isInteger(blurRadius)) {
    throw new Error('prism: window blur radius must be an integer')
  }
  const min = material === 'translucent' ? DESKTOP_WINDOW_BLUR_RADIUS_MIN : 0
  const max = material === 'translucent' ? DESKTOP_WINDOW_BLUR_RADIUS_MAX : 0
  if (blurRadius < min || blurRadius > max) {
    throw new Error(`prism: window blur radius must be between ${min} and ${max} for ${material}`)
  }
  return { material, blurRadius }
}

// Reports whether the adjustable blur took effect. The addon loads on first translucent use;
// when it is missing or the private call is refused the window keeps plain vibrancy.
export function createWindowMaterialApplier(loadAddon: () => WindowMaterialAddon | null) {
  let addon: WindowMaterialAddon | null | undefined
  return (window: MaterialWindow, input: DesktopWindowMaterial): boolean => {
    if (input.material === 'opaque') {
      window.setVibrancy(MAC_WINDOW_VIBRANCY)
      addon?.setBackgroundBlurRadius(window.getNativeWindowHandle(), 0)
      return true
    }
    if (addon === undefined) addon = loadAddon()
    if (addon) {
      window.setVibrancy(null)
      if (addon.setBackgroundBlurRadius(window.getNativeWindowHandle(), input.blurRadius)) return true
    }
    window.setVibrancy(MAC_WINDOW_VIBRANCY)
    return false
  }
}

export function loadWindowMaterialAddon(): WindowMaterialAddon | null {
  const addonPath = app.isPackaged
    ? path.resolve(process.resourcesPath, '..', 'Frameworks', WINDOW_MATERIAL_ADDON_FILE)
    : path.resolve(__dirname, '..', 'native', WINDOW_MATERIAL_ADDON_FILE)
  try {
    const addonModule: { exports: Partial<WindowMaterialAddon> } = { exports: {} }
    process.dlopen(addonModule, addonPath)
    const { setBackgroundBlurRadius } = addonModule.exports
    if (typeof setBackgroundBlurRadius !== 'function') {
      throw new Error('setBackgroundBlurRadius export is missing')
    }
    return { setBackgroundBlurRadius }
  } catch (error) {
    console.warn(`prism: window blur addon unavailable, keeping vibrancy: ${error instanceof Error ? error.message : String(error)}`)
    return null
  }
}

export const applyWindowMaterial = createWindowMaterialApplier(loadWindowMaterialAddon)
