import { createContext, createElement, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react'
import { bridge } from './bridge'
import {
  DEFAULT_THEME_STATE,
  areThemePacksEqual,
  areWindowTranslucenciesEqual,
  buildThemeCssVariables,
  canParseThemeShareString,
  createThemeShareString,
  parseStoredThemeState,
  resetThemeVariant as resetThemeVariantState,
  resolveThemePack,
  resolveThemeVariant,
  serializeThemeState,
  setThemeCodeThemeId,
  setThemeFonts,
  setWindowTranslucency as setWindowTranslucencyState,
  updateChromeTheme,
  updateThemePackFromShareString,
  type ChromeTheme,
  type ThemeCssVariableBuild,
  type ThemeFonts,
  type ThemeMode,
  type ThemePack,
  type ThemeState,
  type ThemeVariant,
  type WindowMaterial,
  type WindowTranslucency,
} from './theme/theme.logic'
import { TYPOGRAPHY_KEY, parseTypography, typographyVariables, type TypographyState } from './theme/typography'

export const THEME_KEY = 'prism-theme'
export const THEME_VARS_KEY = 'prism-theme-vars'
const MEDIA_QUERY = '(prefers-color-scheme: dark)'

export interface AppearanceApi {
  readonly state: ThemeState
  readonly mode: ThemeMode
  readonly setMode: (mode: ThemeMode) => void
  readonly variant: ThemeVariant
  readonly activePack: ThemePack
  readonly packFor: (variant: ThemeVariant) => ThemePack
  readonly translucency: Record<ThemeVariant, WindowTranslucency>
  readonly systemUiFont: boolean
  readonly setSystemUiFont: (enabled: boolean) => void
  readonly setCodeThemeId: (variant: ThemeVariant, codeThemeId: string) => void
  readonly updateThemePack: (variant: ThemeVariant, patch: Partial<ChromeTheme>) => void
  readonly updateThemeFonts: (variant: ThemeVariant, patch: Partial<ThemeFonts>) => void
  readonly setWindowTranslucency: (variant: ThemeVariant, patch: Partial<WindowTranslucency>) => void
  readonly resetThemeVariant: (variant: ThemeVariant) => void
  readonly isDefaultThemePack: (variant: ThemeVariant) => boolean
  readonly exportThemeString: (variant: ThemeVariant) => string
  readonly importThemeString: (value: string, variant: ThemeVariant) => void
  readonly canImportThemeString: (value: string, variant: ThemeVariant) => boolean
  readonly typography: TypographyState
  readonly setTypography: (patch: Partial<TypographyState>) => void
  readonly translucentCapable: boolean
  readonly desktopBlurUnavailable: boolean
}

const AppearanceContext = createContext<AppearanceApi | null>(null)

function readStorage(key: string): string | null {
  try {
    return window.localStorage.getItem(key)
  } catch {
    return null
  }
}

function writeStorage(key: string, value: string): void {
  try {
    window.localStorage.setItem(key, value)
  } catch {
    return
  }
}

function buildFor(state: ThemeState, variant: ThemeVariant, capable: boolean): ThemeCssVariableBuild {
  return buildThemeCssVariables(resolveThemePack(state, variant), variant, {
    electron: capable,
    isMac: capable,
    systemUiFont: state.systemUiFont,
    translucency: state.translucency[variant],
  })
}

// theme-boot.js applies this cache before first paint so the stored look never flashes.
export function themeVarsCache(state: ThemeState, typography: TypographyState, capable: boolean): string {
  return JSON.stringify({
    capable,
    typography: typographyVariables(typography),
    dark: buildFor(state, 'dark', capable),
    light: buildFor(state, 'light', capable),
  })
}

let appliedNames: string[] = []

function applyBuild(root: HTMLElement, build: ThemeCssVariableBuild, extra: Record<string, string>): void {
  const next = { ...build.variables, ...extra }
  for (const name of appliedNames) {
    if (!(name in next) || next[name]!.trim().length === 0) root.style.removeProperty(name)
  }
  appliedNames = []
  for (const [name, value] of Object.entries(next)) {
    if (value.trim().length === 0) continue
    root.style.setProperty(name, value)
    appliedNames.push(name)
  }
  if (build.material === 'translucent') root.dataset.material = 'translucent'
  else delete root.dataset.material
  root.dataset.translucency = build.translucencyScope
}

export function AppearanceProvider({ children }: { readonly children: ReactNode }): JSX.Element {
  const [state, setState] = useState(() => parseStoredThemeState(readStorage(THEME_KEY)))
  const [typography, setTypographyState] = useState(() => parseTypography(readStorage(TYPOGRAPHY_KEY)))
  const [systemDark, setSystemDark] = useState(() => window.matchMedia(MEDIA_QUERY).matches)
  const [blurUnavailable, setBlurUnavailable] = useState(false)
  const capable = document.documentElement.dataset.glass === 'capable'

  useEffect(() => {
    const query = window.matchMedia(MEDIA_QUERY)
    const onChange = (): void => setSystemDark(query.matches)
    query.addEventListener('change', onChange)
    return () => query.removeEventListener('change', onChange)
  }, [])

  const variant = resolveThemeVariant(state.mode, systemDark)
  const build = useMemo(() => buildFor(state, variant, capable), [state, variant, capable])
  const translucency = state.translucency[variant]

  useEffect(() => {
    const root = document.documentElement
    root.dataset.theme = variant
    applyBuild(root, build, typographyVariables(typography))
  }, [build, variant, typography])

  useEffect(() => {
    writeStorage(THEME_KEY, serializeThemeState(state))
    writeStorage(TYPOGRAPHY_KEY, JSON.stringify(typography))
    writeStorage(THEME_VARS_KEY, themeVarsCache(state, typography, capable))
  }, [state, typography, capable])

  useEffect(() => {
    void bridge.window.setTheme(variant).catch(() => undefined)
  }, [variant])

  // Blur is only meaningful while the shell is translucent; a null blur keeps vibrancy.
  const material: WindowMaterial = build.material === 'translucent' && translucency.blur !== null ? 'translucent' : 'opaque'
  const blurRadius = material === 'translucent' && translucency.blur !== null ? translucency.blur : 0
  useEffect(() => {
    if (!capable) return
    let cancelled = false
    bridge.window.setMaterial({ material, blurRadius }).then(
      (applied) => {
        if (!cancelled) setBlurUnavailable(material === 'translucent' && !applied)
      },
      () => undefined,
    )
    return () => {
      cancelled = true
    }
  }, [capable, material, blurRadius])

  const update = useCallback((patch: (current: ThemeState) => ThemeState): void => setState(patch), [])

  const api = useMemo<AppearanceApi>(() => {
    const packFor = (target: ThemeVariant): ThemePack => resolveThemePack(state, target)
    return {
      state,
      mode: state.mode,
      setMode: (mode) => update((current) => ({ ...current, mode })),
      variant,
      activePack: packFor(variant),
      packFor,
      translucency: state.translucency,
      systemUiFont: state.systemUiFont,
      setSystemUiFont: (enabled) => update((current) => ({ ...current, systemUiFont: enabled })),
      setCodeThemeId: (target, id) => update((current) => setThemeCodeThemeId(current, target, id)),
      updateThemePack: (target, patch) => update((current) => updateChromeTheme(current, target, patch)),
      updateThemeFonts: (target, patch) => update((current) => setThemeFonts(current, target, patch)),
      setWindowTranslucency: (target, patch) => update((current) => setWindowTranslucencyState(current, target, patch)),
      resetThemeVariant: (target) => update((current) => resetThemeVariantState(current, target)),
      isDefaultThemePack: (target) =>
        areThemePacksEqual(packFor(target), resolveThemePack(DEFAULT_THEME_STATE, target)) &&
        areWindowTranslucenciesEqual(state.translucency[target], DEFAULT_THEME_STATE.translucency[target]),
      exportThemeString: (target) => createThemeShareString(target, packFor(target)),
      importThemeString: (value, target) => update((current) => updateThemePackFromShareString(current, value, target)),
      canImportThemeString: (value, target) => canParseThemeShareString(value, target),
      typography,
      setTypography: (patch) => setTypographyState((current) => ({ ...current, ...patch })),
      translucentCapable: capable,
      desktopBlurUnavailable: blurUnavailable,
    }
  }, [state, variant, update, typography, capable, blurUnavailable])

  return createElement(AppearanceContext.Provider, { value: api }, children)
}

export function useAppearance(): AppearanceApi {
  const api = useContext(AppearanceContext)
  if (api === null) throw new Error('prism: useAppearance outside AppearanceProvider')
  return api
}

