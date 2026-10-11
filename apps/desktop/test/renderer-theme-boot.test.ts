import { readFileSync } from 'node:fs'
import path from 'node:path'
import { runInNewContext } from 'node:vm'
import { describe, expect, it } from 'vitest'

const script = readFileSync(path.join(__dirname, '..', 'renderer', 'theme-boot.js'), 'utf8')

interface BootResult {
  readonly dataset: DOMStringMap
  readonly style: Record<string, string>
}

function boot(platform: string, withBridge: boolean, storage: Record<string, string> = { 'prism-theme': 'dark' }): BootResult {
  const dataset: DOMStringMap = {}
  const style: Record<string, string> = {}
  const sandbox: Record<string, unknown> = {
    navigator: { platform },
    document: { documentElement: { dataset, style: { setProperty: (k: string, v: string) => (style[k] = v) } } },
    localStorage: { getItem: (key: string) => storage[key] ?? null },
  }
  sandbox.window = { matchMedia: () => ({ matches: false }), ...(withBridge ? { prism: {} } : {}) }
  runInNewContext(script, sandbox)
  return { dataset, style }
}

const cache = (capable: boolean) =>
  JSON.stringify({
    capable,
    typography: { '--ui-scale': '1.1' },
    dark: { material: 'translucent', translucencyScope: 'window', variables: { '--app-canvas': 'glass' } },
    light: { material: 'opaque', translucencyScope: 'none', variables: { '--app-canvas': 'solid' } },
  })

describe('theme boot', () => {
  it('marks only the Electron bridge on macOS as glass capable', () => {
    expect(boot('MacIntel', true).dataset.glass).toBe('capable')
    expect(boot('MacIntel', false).dataset.glass).toBeUndefined()
    expect(boot('Win32', true).dataset.glass).toBeUndefined()
    expect(boot('Linux armv8l', true).dataset.shell).toBe('electron')
  })

  it('resolves the stored theme before first paint', () => {
    expect(boot('MacIntel', false).dataset.theme).toBe('dark')
    expect(boot('MacIntel', false, { 'prism-theme': '{"mode":"light"}' }).dataset.theme).toBe('light')
    expect(boot('MacIntel', false, {}).dataset.theme).toBe('light')
  })

  it('applies the cached theme variables, material and typography for the resolved variant', () => {
    const dark = boot('MacIntel', true, { 'prism-theme': 'dark', 'prism-theme-vars': cache(true) })
    expect(dark.style['--app-canvas']).toBe('glass')
    expect(dark.style['--ui-scale']).toBe('1.1')
    expect(dark.dataset.material).toBe('translucent')
    expect(dark.dataset.translucency).toBe('window')
    const light = boot('MacIntel', true, { 'prism-theme': 'light', 'prism-theme-vars': cache(true) })
    expect(light.style['--app-canvas']).toBe('solid')
    expect(light.dataset.material).toBeUndefined()
  })

  it('ignores a cache built for a different capability', () => {
    const result = boot('Linux armv8l', false, { 'prism-theme': 'dark', 'prism-theme-vars': cache(true) })
    expect(result.style['--app-canvas']).toBeUndefined()
    expect(result.dataset.material).toBeUndefined()
  })
})
