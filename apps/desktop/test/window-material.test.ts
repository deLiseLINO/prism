import { describe, expect, it, vi } from 'vitest'

vi.mock('electron', () => ({ app: { isPackaged: false } }))

import { MAC_WINDOW_VIBRANCY, createWindowMaterialApplier, parseWindowMaterialRequest } from '../main/window-material'

function fakeWindow() {
  const handle = Buffer.alloc(8)
  return { handle, getNativeWindowHandle: vi.fn(() => handle), setVibrancy: vi.fn() }
}

describe('parseWindowMaterialRequest', () => {
  it('accepts valid requests', () => {
    expect(parseWindowMaterialRequest({ material: 'translucent', blurRadius: 64 })).toEqual({ material: 'translucent', blurRadius: 64 })
    expect(parseWindowMaterialRequest({ material: 'translucent', blurRadius: 1 })).toEqual({ material: 'translucent', blurRadius: 1 })
    expect(parseWindowMaterialRequest({ material: 'opaque', blurRadius: 0 })).toEqual({ material: 'opaque', blurRadius: 0 })
  })

  it('rejects everything else', () => {
    for (const bad of [null, 'translucent', 3, { material: 'glass', blurRadius: 4 }, { material: 'translucent' }]) {
      expect(() => parseWindowMaterialRequest(bad)).toThrow(/prism:/)
    }
    expect(() => parseWindowMaterialRequest({ material: 'translucent', blurRadius: 65 })).toThrow(/between 1 and 64/)
    expect(() => parseWindowMaterialRequest({ material: 'translucent', blurRadius: 0 })).toThrow(/between 1 and 64/)
    expect(() => parseWindowMaterialRequest({ material: 'translucent', blurRadius: 3.5 })).toThrow(/integer/)
    expect(() => parseWindowMaterialRequest({ material: 'opaque', blurRadius: 5 })).toThrow(/between 0 and 0/)
    expect(() => parseWindowMaterialRequest({ material: 'opaque', blurRadius: Number.NaN })).toThrow(/integer/)
  })
})

describe('createWindowMaterialApplier', () => {
  it('drops vibrancy and sets the blur radius when translucent', () => {
    const addon = { setBackgroundBlurRadius: vi.fn(() => true) }
    const apply = createWindowMaterialApplier(() => addon)
    const window = fakeWindow()
    expect(apply(window, { material: 'translucent', blurRadius: 20 })).toBe(true)
    expect(window.setVibrancy).toHaveBeenLastCalledWith(null)
    expect(addon.setBackgroundBlurRadius).toHaveBeenLastCalledWith(window.handle, 20)
    expect(apply(window, { material: 'opaque', blurRadius: 0 })).toBe(true)
    expect(window.setVibrancy).toHaveBeenLastCalledWith(MAC_WINDOW_VIBRANCY)
    expect(addon.setBackgroundBlurRadius).toHaveBeenLastCalledWith(window.handle, 0)
  })

  it('keeps vibrancy and loads the addon once when it is unavailable', () => {
    const loadAddon = vi.fn(() => null)
    const apply = createWindowMaterialApplier(loadAddon)
    const window = fakeWindow()
    expect(apply(window, { material: 'translucent', blurRadius: 20 })).toBe(false)
    expect(apply(window, { material: 'translucent', blurRadius: 10 })).toBe(false)
    expect(loadAddon).toHaveBeenCalledTimes(1)
    expect(window.setVibrancy).not.toHaveBeenCalledWith(null)
  })

  it('restores vibrancy when the window server refuses the blur', () => {
    const apply = createWindowMaterialApplier(() => ({ setBackgroundBlurRadius: () => false }))
    const window = fakeWindow()
    expect(apply(window, { material: 'translucent', blurRadius: 20 })).toBe(false)
    expect(window.setVibrancy).toHaveBeenLastCalledWith(MAC_WINDOW_VIBRANCY)
  })
})
