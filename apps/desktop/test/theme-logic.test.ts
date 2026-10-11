import { describe, expect, it } from 'vitest'
import { DESKTOP_WINDOW_BLUR_RADIUS_MAX, DESKTOP_WINDOW_BLUR_RADIUS_MIN } from '@prism/contracts'
import {
  CODE_THEME_OPTIONS,
  DEFAULT_THEME_STATE,
  WINDOW_TRANSLUCENCY_OPACITY_MIN,
  buildThemeCssVariables,
  canParseThemeShareString,
  createThemeShareString,
  getCodeThemeSeed,
  isCodeThemeAvailable,
  normalizeWindowTranslucency,
  parseStoredThemeState,
  parseThemeShareString,
  resolveThemePack,
  setThemeCodeThemeId,
  updateChromeTheme,
  updateThemePackFromShareString,
} from '../renderer/src/theme/theme.logic'
import { THEME_SEED_CATALOG } from '../renderer/src/theme/theme.seed.generated'

const HEX = /^#[0-9a-f]{6}$/

describe('theme catalog', () => {
  it('lists the 27 available packs', () => {
    expect(CODE_THEME_OPTIONS.map((option) => option.id)).toEqual([
      'absolutely', 'ayu', 'catppuccin', 'codex', 'dracula', 'everforest', 'github', 'gruvbox',
      'linear', 'lobster', 'material', 'matrix', 'monokai', 'night-owl', 'nord', 'notion', 'one', 'oscurange',
      'proof', 'raycast', 'rose-pine', 'sentry', 'solarized', 'temple', 'tokyo-night', 'vercel', 'vscode-plus',
    ])
  })

  it('ships every declared variant with valid colors', () => {
    for (const option of CODE_THEME_OPTIONS) {
      const entry = THEME_SEED_CATALOG[option.id]
      expect(entry, option.id).toBeDefined()
      expect(Object.keys(entry!).sort(), option.id).toEqual([...option.variants].sort())
      for (const variant of option.variants) {
        const seed = entry![variant]!
        for (const color of [seed.accent, seed.ink, seed.surface, ...Object.values(seed.semanticColors)]) {
          expect(color, `${option.id}/${variant}`).toMatch(HEX)
        }
        expect('contrast' in seed).toBe(false)
      }
    }
  })

  it('carries the literal values of a few packs', () => {
    expect(getCodeThemeSeed('nord', 'dark')).toMatchObject({ accent: '#88c0d0', ink: '#d8dee9', surface: '#2e3440' })
    expect(getCodeThemeSeed('dracula', 'dark')).toMatchObject({ accent: '#ff79c6', ink: '#f8f8f2', surface: '#282a36' })
    expect(getCodeThemeSeed('codex', 'light')).toMatchObject({ accent: '#0169cc', ink: '#0d0d0d', surface: '#ffffff' })
    expect(THEME_SEED_CATALOG.unknown).toBeUndefined()
    expect(isCodeThemeAvailable('unknown', 'dark')).toBe(false)
    expect(isCodeThemeAvailable('dracula', 'light')).toBe(false)
    expect(isCodeThemeAvailable('proof', 'light')).toBe(true)
  })

  it('defaults to the GitHub pack in both variants', () => {
    expect(DEFAULT_THEME_STATE.codeThemeIds).toEqual({ dark: 'github', light: 'github' })
    expect(DEFAULT_THEME_STATE.mode).toBe('system')
    expect(resolveThemePack(DEFAULT_THEME_STATE, 'dark').theme).toMatchObject({ accent: '#1f6feb', surface: '#0d1117', ink: '#e6edf3' })
    expect(resolveThemePack(DEFAULT_THEME_STATE, 'light').theme).toMatchObject({ accent: '#0969da', surface: '#ffffff', ink: '#1f2328' })
    expect(DEFAULT_THEME_STATE.translucency.dark).toEqual({ opacity: 90, blur: 43, sidebarOnly: false })
    expect(DEFAULT_THEME_STATE.translucency.light).toEqual({ opacity: 90, blur: 43, sidebarOnly: false })
    expect(parseStoredThemeState(null)).toEqual(DEFAULT_THEME_STATE)
  })

  it('applies a pack\'s seed when it is selected', () => {
    const next = setThemeCodeThemeId(DEFAULT_THEME_STATE, 'dark', 'nord')
    expect(resolveThemePack(next, 'dark')).toMatchObject({ codeThemeId: 'nord', theme: { accent: '#88c0d0', surface: '#2e3440' } })
    expect(setThemeCodeThemeId(DEFAULT_THEME_STATE, 'dark', 'proof').codeThemeIds.dark).toBe('github')
  })
})

describe('stored state parsing', () => {
  it('falls back for junk and migrates a bare mode string', () => {
    expect(parseStoredThemeState(null)).toEqual(DEFAULT_THEME_STATE)
    expect(parseStoredThemeState('{broken')).toEqual(DEFAULT_THEME_STATE)
    expect(parseStoredThemeState('dark').mode).toBe('dark')
  })

  it('normalizes fields independently', () => {
    const state = parseStoredThemeState(
      JSON.stringify({
        mode: 'light',
        chromeThemes: { dark: { accent: 'nope', surface: '#ABCDEF', contrast: 250 } },
        codeThemeIds: { dark: 'not-a-pack', light: 'gruvbox' },
        translucency: { dark: { opacity: 3, blur: 500, sidebarOnly: true } },
      }),
    )
    expect(state.mode).toBe('light')
    expect(state.chromeThemes.dark.surface).toBe('#abcdef')
    expect(state.chromeThemes.dark.accent).toBe('#339cff')
    expect('contrast' in state.chromeThemes.dark).toBe(false)
    expect(state.codeThemeIds).toEqual({ dark: 'codex', light: 'gruvbox' })
    expect(state.translucency.dark).toEqual({ opacity: WINDOW_TRANSLUCENCY_OPACITY_MIN, blur: DESKTOP_WINDOW_BLUR_RADIUS_MAX, sidebarOnly: true })
  })

  it('clamps blur, keeps null as automatic and rounds opacity', () => {
    expect(normalizeWindowTranslucency({ opacity: 55.6, blur: 0 }, 'dark')).toMatchObject({ opacity: 56, blur: DESKTOP_WINDOW_BLUR_RADIUS_MIN })
    expect(normalizeWindowTranslucency({ blur: null }, 'dark').blur).toBeNull()
    expect(normalizeWindowTranslucency({ blur: 'x' }, 'light').blur).toBe(43)
  })
})

describe('buildThemeCssVariables', () => {
  const pack = resolveThemePack(DEFAULT_THEME_STATE, 'dark')

  it('is opaque unless the host is electron on mac', () => {
    expect(buildThemeCssVariables(pack, 'dark').material).toBe('opaque')
    expect(buildThemeCssVariables(pack, 'dark', { electron: true, isMac: false }).material).toBe('opaque')
    expect(buildThemeCssVariables(pack, 'dark', { electron: true, isMac: true }).material).toBe('translucent')
  })

  it('keeps the window opaque when the pack asks for opaque windows', () => {
    const solid = updateChromeTheme(DEFAULT_THEME_STATE, 'dark', { opaqueWindows: true })
    const build = buildThemeCssVariables(resolveThemePack(solid, 'dark'), 'dark', { electron: true, isMac: true })
    expect(build.material).toBe('opaque')
    expect(build.translucencyScope).toBe('none')
  })

  it('maps opacity to the glass coat and sidebarOnly to the scope', () => {
    const whole = buildThemeCssVariables(pack, 'dark', { electron: true, isMac: true, translucency: { opacity: 35, blur: 30, sidebarOnly: false } })
    expect(whole.translucencyScope).toBe('window')
    expect(whole.variables['--app-canvas']).toContain(' 35%, transparent)')
    expect(whole.variables['--app-panel-surface']).toBe('color-mix(in srgb, #000 16%, transparent)')
    const sidebar = buildThemeCssVariables(pack, 'dark', { electron: true, isMac: true, translucency: { opacity: 35, blur: 30, sidebarOnly: true } })
    expect(sidebar.translucencyScope).toBe('sidebar')
    expect(sidebar.variables['--app-panel-surface']).toBe(pack.theme.surface)
  })

  it('uses solid colors when opaque', () => {
    const build = buildThemeCssVariables(pack, 'dark')
    expect(build.variables['--app-canvas']).toMatch(/^#[0-9a-f]{6}$/)
    expect(build.variables['--app-panel-surface']).toBe('#0d1117')
  })

  it('derives light chrome from ink and surface', () => {
    const light = buildThemeCssVariables(resolveThemePack(DEFAULT_THEME_STATE, 'light'), 'light')
    expect(light.variables['--color-text-foreground']).toBe('#1f2328')
    expect(light.variables['--color-border']).toBe('rgba(31, 35, 40, 0.069)')
    expect(light.variables['--app-panel-surface']).toBe('#ffffff')
  })

  it('emits the accent as --accent-background and leaves --accent to Prism', () => {
    const vars = buildThemeCssVariables(pack, 'dark').variables
    expect(vars['--accent']).toBeUndefined()
    expect(vars['--accent-background']).toBeDefined()
    expect(vars['--color-accent-blue']).toBe('#1f6feb')
    expect(vars['--app-accent-contrast']).toBe('#ffffff')
  })

  it('clears the UI font when the system font is in use', () => {
    const withFont = { ...pack, theme: { ...pack.theme, fonts: { ui: 'Inter', code: 'Fira Code' } } }
    expect(buildThemeCssVariables(withFont, 'dark', { systemUiFont: true }).variables['--theme-font-ui-family']).toBe('')
    expect(buildThemeCssVariables(withFont, 'dark', { systemUiFont: true }).variables['--theme-font-code-family']).toBe('')
    expect(buildThemeCssVariables(withFont, 'dark', { systemUiFont: false }).variables['--theme-font-ui-family']).toBe('Inter')
    expect(buildThemeCssVariables(withFont, 'dark').variables['--theme-font-code-family']).toBe('"Fira Code", ui-monospace, "SF Mono", SFMono-Regular, Menlo, "Cascadia Mono", Consolas, "Liberation Mono", "JetBrains Mono", monospace')
  })
})

describe('theme share strings', () => {
  it('round-trips a customized variant', () => {
    const state = updateChromeTheme(setThemeCodeThemeId(DEFAULT_THEME_STATE, 'light', 'github'), 'light', { accent: '#123456' })
    const pack = resolveThemePack(state, 'light')
    const text = createThemeShareString('light', pack)
    expect(text.startsWith('codex-theme-v1:')).toBe(true)
    expect(parseThemeShareString(text)).toEqual({ codeThemeId: 'github', theme: pack.theme, variant: 'light' })
    const imported = updateThemePackFromShareString(DEFAULT_THEME_STATE, text, 'light')
    expect(resolveThemePack(imported, 'light')).toEqual(pack)
  })

  it('accepts and ignores a legacy contrast field', () => {
    const pack = resolveThemePack(DEFAULT_THEME_STATE, 'dark')
    const legacy = `codex-theme-v1:${JSON.stringify({ codeThemeId: 'github', variant: 'dark', theme: { ...pack.theme, contrast: 55 } })}`
    const parsed = parseThemeShareString(legacy)
    expect(parsed.theme).toEqual(pack.theme)
    expect('contrast' in parsed.theme).toBe(false)
    expect(createThemeShareString('dark', pack)).not.toContain('contrast')
  })

  it('rejects wrong prefix, variant mismatch, bad colors and unavailable packs', () => {
    const pack = resolveThemePack(DEFAULT_THEME_STATE, 'dark')
    const text = createThemeShareString('dark', pack)
    expect(() => parseThemeShareString('nope')).toThrow(/must start with/)
    expect(canParseThemeShareString(text, 'dark')).toBe(true)
    expect(canParseThemeShareString(text, 'light')).toBe(false)
    expect(() => parseThemeShareString(text.replace('#1f6feb', '#1f6fe'))).toThrow(/6-digit hex/)
    expect(() => parseThemeShareString(text.replace('"github"', '"nope"'))).toThrow(/not available/)
  })
})
