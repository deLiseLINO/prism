import { useEffect, useRef, useState, type CSSProperties, type ReactNode } from 'react'
import { DESKTOP_WINDOW_BLUR_RADIUS_MAX, DESKTOP_WINDOW_BLUR_RADIUS_MIN } from '@prism/contracts'
import {
  CODE_THEME_OPTIONS,
  DEFAULT_THEME_STATE,
  VIBRANCY_EQUIVALENT_BLUR_RADIUS,
  WINDOW_TRANSLUCENCY_OPACITY_MIN,
  getAvailableCodeThemes,
  getCodeThemeSeed,
  resolveThemePack,
  buildThemeCssVariables,
  type ChromeTheme,
  type ThemeMode,
  type ThemeVariant,
} from '../theme/theme.logic'
import {
  DEFAULT_FONT_SIZE_PX,
  MAX_FONT_SIZE_PX,
  MIN_FONT_SIZE_PX,
  normalizeFontSizePx,
} from '../theme/typography'
import { bridge } from '../bridge'
import { useAppearance } from '../useAppearance'

const HEX_COLOR_RE = /^#[0-9a-fA-F]{6}$/
const COLOR_COMMIT_DELAY_MS = 220

const MODE_CHOICES: readonly { readonly value: ThemeMode; readonly label: string }[] = [
  { value: 'system', label: 'System' },
  { value: 'light', label: 'Light' },
  { value: 'dark', label: 'Dark' },
]

export function AppearanceView(): JSX.Element {
  const { mode, setMode, variant, systemUiFont, setSystemUiFont, typography, setTypography } = useAppearance()
  const order: readonly ThemeVariant[] = variant === 'dark' ? ['dark', 'light'] : ['light', 'dark']

  return (
    <section className="screen st-page" aria-labelledby="h-appearance">
      <div className="screen-head" style={{ '--i': 0 } as CSSProperties}>
        <div>
          <h1 id="h-appearance">Appearance</h1>
        </div>
      </div>

      <SettingsSectionShell title="Theme" action={mode !== 'system' ? <ResetButton label="theme" onClick={() => setMode('system')} /> : null}>
        <ModePicker value={mode} onChange={setMode} />
        <div className="stack" style={{ gap: 12 }}>
          {order.map((target) => (
            <ThemePackEditor key={target} variant={target} isActive={variant === target} />
          ))}
        </div>
      </SettingsSectionShell>

      <SettingsSectionShell title="Typography and spacing">
        <div className="st-card">
          <SettingsRow
            title="Use system UI font"
            description="Ignore the theme's custom UI font and render the interface with the native system font (SF Pro on macOS)."
            resetAction={!systemUiFont ? <ResetButton label="system UI font" onClick={() => setSystemUiFont(true)} /> : null}
            control={<Switch checked={systemUiFont} onChange={setSystemUiFont} label="Use system UI font" />}
          />
          <SettingsRow
            title="Base font size"
            description="Adjust the app text base in pixels. UI typography scales proportionally from this value."
            resetAction={
              typography.fontSizePx !== DEFAULT_FONT_SIZE_PX ? (
                <ResetButton label="base font size" onClick={() => setTypography({ fontSizePx: DEFAULT_FONT_SIZE_PX })} />
              ) : null
            }
            control={
              <div className="st-row__control">
                <input
                  type="number"
                  className="st-input st-input--num"
                  min={MIN_FONT_SIZE_PX}
                  max={MAX_FONT_SIZE_PX}
                  step={1}
                  inputMode="numeric"
                  value={String(typography.fontSizePx)}
                  aria-label="Base font size in pixels"
                  onChange={(event) => {
                    const next = event.target.value.trim()
                    if (next.length === 0) return
                    setTypography({ fontSizePx: normalizeFontSizePx(Number(next)) })
                  }}
                />
                <span className="st-note">px</span>
              </div>
            }
          />
        </div>
      </SettingsSectionShell>
    </section>
  )
}

// ── Settings primitives ──

function SettingsSectionShell({ title, action, children }: { readonly title: string; readonly action?: ReactNode; readonly children: ReactNode }): JSX.Element {
  return (
    <section className="st-section">
      <div className="st-section__head">
        <h2 className="st-section__title">{title}</h2>
        {action}
      </div>
      {children}
    </section>
  )
}

function SettingsRow({ title, description, resetAction, control }: {
  readonly title: string
  readonly description: string
  readonly resetAction?: ReactNode
  readonly control: ReactNode
}): JSX.Element {
  return (
    <div className="st-row">
      <div className="st-row__text">
        <div className="st-row__title">
          <h3 style={{ font: 'inherit' }}>{title}</h3>
          <span className="st-reset-slot" style={{ display: 'inline-flex', width: 20, height: 20, alignItems: 'center', justifyContent: 'center' }}>{resetAction}</span>
        </div>
        <p className="st-row__desc">{description}</p>
      </div>
      <div className="st-row__control">{control}</div>
    </div>
  )
}

function ResetButton({ label, onClick }: { readonly label: string; readonly onClick: () => void }): JSX.Element {
  return (
    <button type="button" className="st-reset" aria-label={`Reset ${label} to default`} title="Reset to default" onClick={onClick}>
      <ResetGlyph />
    </button>
  )
}

function ResetGlyph(): JSX.Element {
  return (
    <svg aria-hidden="true" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="M3 12a9 9 0 1 0 3-6.7" />
      <polyline points="3 4 3 10 9 10" />
    </svg>
  )
}

function Switch({ checked, onChange, label }: { readonly checked: boolean; readonly onChange: (next: boolean) => void; readonly label: string }): JSX.Element {
  return <button type="button" role="switch" className="st-switch" aria-checked={checked} aria-label={label} onClick={() => onChange(!checked)} />
}

function SegmentedControl<T extends string>({ value, options, ariaLabel, onChange, disabledValues }: {
  readonly value: T
  readonly options: readonly { readonly value: T; readonly label: string }[]
  readonly ariaLabel: string
  readonly onChange: (value: T) => void
  readonly disabledValues?: readonly T[]
}): JSX.Element {
  return (
    <div role="radiogroup" aria-label={ariaLabel} className="st-seg">
      {options.map((option) => (
        <button
          key={option.value}
          type="button"
          role="radio"
          className="st-seg__btn"
          aria-checked={option.value === value}
          disabled={disabledValues?.includes(option.value) === true}
          onClick={() => onChange(option.value)}
        >
          {option.label}
        </button>
      ))}
    </div>
  )
}

// ── Theme mode picker ──

const MOCKUP_COLORS = {
  light: { backdrop: '#e9e9e9', panel: '#f6f6f6', headerBar: '#cfcfcf', headerBarSoft: '#e0e0e0', card: '#ffffff', rowBar: '#e3e3e3', hairline: '#efefef' },
  dark: { backdrop: '#5f5f5f', panel: '#2c2c2c', headerBar: '#a6a6a6', headerBarSoft: '#7d7d7d', card: '#3a3a3a', rowBar: '#707070', hairline: '#4d4d4d' },
} as const

function MockupSurface({ tone }: { readonly tone: ThemeVariant }): JSX.Element {
  const colors = MOCKUP_COLORS[tone]
  return (
    <div aria-hidden="true" className="st-mock" style={{ backgroundColor: colors.backdrop }}>
      <div className="st-mock__panel" style={{ backgroundColor: colors.panel }}>
        <div className="st-mock__head">
          <div className="st-mock__bar" style={{ height: 4, width: '38%', backgroundColor: colors.headerBar }} />
          <div className="st-mock__bar" style={{ height: 3, width: '55%', backgroundColor: colors.headerBarSoft }} />
        </div>
        <div className="st-mock__card" style={{ backgroundColor: colors.card }}>
          {[0, 1, 2].map((row) => (
            <div key={row} className="st-mock__row">
              <i style={{ backgroundColor: colors.rowBar }} />
              <b style={{ backgroundColor: colors.hairline }} />
            </div>
          ))}
        </div>
      </div>
    </div>
  )
}

function ModeMockup({ mode }: { readonly mode: ThemeMode }): JSX.Element {
  if (mode !== 'system') return <MockupSurface tone={mode} />
  return (
    <div aria-hidden="true" className="st-mock">
      <MockupSurface tone="light" />
      <div className="st-mock--system-dark">
        <MockupSurface tone="dark" />
      </div>
    </div>
  )
}

function ModePicker({ value, onChange }: { readonly value: ThemeMode; readonly onChange: (mode: ThemeMode) => void }): JSX.Element {
  return (
    <div role="radiogroup" aria-label="Theme preference" className="st-modes">
      {MODE_CHOICES.map((choice) => (
        <button key={choice.value} type="button" role="radio" aria-checked={choice.value === value} className="st-mode" onClick={() => onChange(choice.value)}>
          <span className="st-mode__frame">
            <span className="st-mode__art">
              <ModeMockup mode={choice.value} />
            </span>
          </span>
          <span>{choice.label}</span>
        </button>
      ))}
    </div>
  )
}

// ── Theme pack editor ──

const WINDOW_MATERIAL_OPTIONS = [
  { value: 'solid', label: 'Solid' },
  { value: 'translucent', label: 'Translucent' },
] as const

function ThemePackEditor({ variant, isActive }: { readonly variant: ThemeVariant; readonly isActive: boolean }): JSX.Element {
  const {
    mode,
    setMode,
    packFor,
    exportThemeString,
    importThemeString,
    isDefaultThemePack,
    resetThemeVariant,
    setCodeThemeId,
    systemUiFont,
    desktopBlurUnavailable,
    setWindowTranslucency,
    translucency: translucencyByVariant,
    updateThemePack,
    updateThemeFonts,
    translucentCapable,
  } = useAppearance()
  const pack = packFor(variant)
  const theme = pack.theme
  const translucency = translucencyByVariant[variant]
  const previewVariables = buildThemeCssVariables(pack, variant, { systemUiFont }).variables
  const defaultTheme = resolveThemePack(DEFAULT_THEME_STATE, variant).theme
  const codeThemeLabel = CODE_THEME_OPTIONS.find((option) => option.id === pack.codeThemeId)?.label ?? pack.codeThemeId
  const isPristine = isDefaultThemePack(variant)
  const titleLabel = variant === 'dark' ? 'Dark theme' : 'Light theme'
  const [copied, setCopied] = useState<'copied' | 'failed' | null>(null)
  const contextLabel = isActive
    ? mode === 'system'
      ? `System is currently using this ${variant} slot.`
      : 'This is the active theme right now.'
    : mode === 'system'
      ? `Used when your system switches to ${variant}.`
      : `Inactive while the app is locked to ${mode}.`

  const copy = async (): Promise<void> => {
    try {
      await bridge.shell.writeClipboard(exportThemeString(variant))
      setCopied('copied')
    } catch {
      setCopied('failed')
    }
  }

  return (
    <div className="st-card" style={{ ['--tc-radius' as string]: '12px' }}>
      <div className="st-head" style={{ borderTop: 0 }}>
        <div className="st-head__title">
          <h3 style={{ font: 'inherit' }}>{titleLabel}</h3>
          {!isPristine ? (
            <button type="button" className="st-text-action" style={{ fontSize: 12 }} onClick={() => resetThemeVariant(variant)}>
              Reset
            </button>
          ) : null}
        </div>
        <div className="st-head__actions">
          <ImportThemeDialog variant={variant} onImport={(value) => importThemeString(value, variant)} />
          <button type="button" className="st-text-action" onClick={() => void copy()}>
            {copied === 'copied' ? 'Copied' : copied === 'failed' ? 'Copy failed' : 'Copy'}
          </button>
          <CodeThemePicker variant={variant} value={pack.codeThemeId} theme={theme} onChange={(id) => setCodeThemeId(variant, id)} ariaLabel={`${titleLabel} code theme`} />
        </div>
      </div>
      <div className="st-context" style={{ borderTop: 0 }}>
        <span>{contextLabel}</span>
        {!isActive ? (
          <button type="button" className="btn btn--ghost btn--sm" onClick={() => setMode(variant)}>
            Use {variant} theme
          </button>
        ) : null}
      </div>
      <div className="st-preview-wrap">
        <div
          role="img"
          aria-label={`${titleLabel} preview: ${codeThemeLabel}`}
          className="st-preview"
          style={{ ...(previewVariables as CSSProperties), colorScheme: variant }}
        >
          <span className="st-preview__label">{codeThemeLabel}</span>
          <span className="st-preview__accent">Accent preview</span>
        </div>
      </div>

      <div className="st-stack">
        <ThemeRow label="Accent">
          <ColorPill
            color={theme.accent}
            ariaLabel={`${titleLabel} accent color`}
            onChange={(next) => updateThemePack(variant, { accent: next })}
            onReset={theme.accent !== defaultTheme.accent ? () => updateThemePack(variant, { accent: defaultTheme.accent }) : undefined}
          />
        </ThemeRow>
        <ThemeRow label="Background">
          <ColorPill
            color={theme.surface}
            ariaLabel={`${titleLabel} background color`}
            onChange={(next) => updateThemePack(variant, { surface: next })}
            onReset={theme.surface !== defaultTheme.surface ? () => updateThemePack(variant, { surface: defaultTheme.surface }) : undefined}
          />
        </ThemeRow>
        <ThemeRow label="Foreground">
          <ColorPill
            color={theme.ink}
            ariaLabel={`${titleLabel} foreground color`}
            onChange={(next) => updateThemePack(variant, { ink: next })}
            onReset={theme.ink !== defaultTheme.ink ? () => updateThemePack(variant, { ink: defaultTheme.ink }) : undefined}
          />
        </ThemeRow>
        <ThemeRow label="UI font">
          <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'flex-end', gap: 4 }}>
            <FontInput
              value={theme.fonts.ui ?? ''}
              placeholder="System default"
              ariaLabel={`${titleLabel} UI font`}
              onChange={(next) => updateThemeFonts(variant, { ui: next.length > 0 ? next : null })}
            />
            {systemUiFont ? <span className="st-note">Use system UI font is on; theme fonts are not applied.</span> : null}
          </div>
        </ThemeRow>
        <ThemeRow label="Code font">
          <FontInput
            value={theme.fonts.code ?? ''}
            placeholder={'"SF Mono"'}
            ariaLabel={`${titleLabel} code font`}
            mono
            onChange={(next) => updateThemeFonts(variant, { code: next.length > 0 ? next : null })}
          />
        </ThemeRow>
        <div>
          <ThemeRow label="Window">
            <SegmentedControl
              value={theme.opaqueWindows ? 'solid' : 'translucent'}
              ariaLabel={`${titleLabel} window material`}
              options={WINDOW_MATERIAL_OPTIONS}
              disabledValues={translucentCapable ? [] : ['translucent']}
              onChange={(value) => updateThemePack(variant, { opaqueWindows: value === 'solid' })}
            />
          </ThemeRow>
          {!translucentCapable ? (
            <p className="st-note" style={{ padding: '0 12px 10px' }}>Window translucency needs the macOS desktop app</p>
          ) : null}
          {!theme.opaqueWindows ? (
            <div className="st-stack" style={{ borderTop: '1px solid var(--border)' }}>
              <ThemeRow label="Sidebar only">
                <Switch
                  checked={translucency.sidebarOnly}
                  onChange={(checked) => setWindowTranslucency(variant, { sidebarOnly: checked })}
                  label={`${titleLabel} translucent sidebar only`}
                />
              </ThemeRow>
              <ThemeRow label="Opacity">
                <ThemeSlider
                  value={translucency.opacity}
                  min={WINDOW_TRANSLUCENCY_OPACITY_MIN}
                  max={100}
                  suffix="%"
                  onChange={(next) => setWindowTranslucency(variant, { opacity: next })}
                  ariaLabel={`${titleLabel} translucency opacity`}
                />
              </ThemeRow>
              <ThemeRow label="Blur">
                {translucency.blur !== null ? (
                  <button
                    type="button"
                    className="btn btn--ghost btn--sm"
                    onClick={() => setWindowTranslucency(variant, { blur: null })}
                    aria-label={`${titleLabel} automatic background blur`}
                  >
                    {isActive && desktopBlurUnavailable ? 'Unavailable, use Auto' : 'Auto'}
                  </button>
                ) : null}
                <ThemeSlider
                  value={translucency.blur ?? VIBRANCY_EQUIVALENT_BLUR_RADIUS}
                  valueLabel={translucency.blur === null ? 'Auto' : undefined}
                  min={DESKTOP_WINDOW_BLUR_RADIUS_MIN}
                  max={DESKTOP_WINDOW_BLUR_RADIUS_MAX}
                  onChange={(next) => setWindowTranslucency(variant, { blur: next })}
                  ariaLabel={`${titleLabel} background blur`}
                />
              </ThemeRow>
            </div>
          ) : null}
        </div>
      </div>
    </div>
  )
}

function ThemeRow({ label, children }: { readonly label: string; readonly children: ReactNode }): JSX.Element {
  return (
    <div className="st-row">
      <span className="st-row__label">{label}</span>
      <div className="st-row__control">{children}</div>
    </div>
  )
}

function readableTextColor(hex: string, alpha = 1): string {
  const value = HEX_COLOR_RE.test(hex) ? hex.slice(1) : null
  if (value === null) return alpha === 1 ? '#ffffff' : `rgba(255,255,255,${alpha})`
  const luminance = (0.299 * parseInt(value.slice(0, 2), 16) + 0.587 * parseInt(value.slice(2, 4), 16) + 0.114 * parseInt(value.slice(4, 6), 16)) / 255
  if (luminance > 0.6) return alpha === 1 ? '#1a1c1f' : `rgba(26,28,31,${alpha})`
  return alpha === 1 ? '#ffffff' : `rgba(255,255,255,${alpha})`
}

function mixColor(fromHex: string, toHex: string, amount: number): string {
  if (!HEX_COLOR_RE.test(fromHex) || !HEX_COLOR_RE.test(toHex)) return fromHex
  const channels = (hex: string): number[] => [1, 3, 5].map((offset) => parseInt(hex.slice(offset, offset + 2), 16))
  const from = channels(fromHex)
  const to = channels(toHex)
  const mixed = from.map((channel, index) => Math.round(channel + (to[index]! - channel) * amount))
  return `rgb(${mixed.join(', ')})`
}

function useOutsideClose(open: boolean, close: () => void): React.RefObject<HTMLDivElement | null> {
  const ref = useRef<HTMLDivElement | null>(null)
  useEffect(() => {
    if (!open) return
    const onPointer = (event: PointerEvent): void => {
      if (ref.current !== null && !ref.current.contains(event.target as Node)) close()
    }
    const onKey = (event: KeyboardEvent): void => {
      if (event.key === 'Escape') close()
    }
    document.addEventListener('pointerdown', onPointer)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('pointerdown', onPointer)
      document.removeEventListener('keydown', onKey)
    }
  }, [open, close])
  return ref
}

function CodeThemeBadge({ theme }: { readonly theme: ChromeTheme }): JSX.Element {
  return (
    <span aria-hidden="true" className="st-badge" style={{ backgroundColor: theme.surface, borderColor: mixColor(theme.surface, theme.ink, 0.16), color: theme.accent }}>
      Aa
    </span>
  )
}

function CodeThemePicker({ variant, value, theme, onChange, ariaLabel }: {
  readonly variant: ThemeVariant
  readonly value: string
  readonly theme: ChromeTheme
  readonly onChange: (id: string) => void
  readonly ariaLabel: string
}): JSX.Element {
  const [open, setOpen] = useState(false)
  const ref = useOutsideClose(open, () => setOpen(false))
  const label = CODE_THEME_OPTIONS.find((option) => option.id === value)?.label ?? value
  return (
    <div className="st-picker" ref={ref}>
      <button type="button" className="st-picker__btn" aria-haspopup="listbox" aria-expanded={open} aria-label={ariaLabel} onClick={() => setOpen((current) => !current)}>
        <CodeThemeBadge theme={theme} />
        <span className="st-picker__label">{label}</span>
        <svg aria-hidden="true" width="10" height="10" viewBox="0 0 10 10" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"><path d="m2 4 3 3 3-3" /></svg>
      </button>
      {open ? (
        <div className="st-picker__menu" role="listbox" aria-label={ariaLabel}>
          {getAvailableCodeThemes(variant).map((option) => (
            <button
              key={option.id}
              type="button"
              role="option"
              className="st-picker__opt"
              aria-selected={option.id === value}
              onClick={() => {
                onChange(option.id)
                setOpen(false)
              }}
            >
              <CodeThemeBadge theme={getCodeThemeSeed(option.id, variant)} />
              <span>{option.label}</span>
            </button>
          ))}
        </div>
      ) : null}
    </div>
  )
}

function ColorPill({ color, ariaLabel, onChange, onReset }: {
  readonly color: string
  readonly ariaLabel: string
  readonly onChange: (next: string) => void
  readonly onReset?: (() => void) | undefined
}): JSX.Element {
  const [open, setOpen] = useState(false)
  const [draftRaw, setDraft] = useState<string | null>(null)
  const timer = useRef<number | null>(null)
  const pending = useRef<string | null>(null)
  const colorRef = useRef(color)
  const ref = useOutsideClose(open, () => close())
  // Once the committed color catches up with the draft, the draft dissolves in the same render.
  const draft = draftRaw === color ? null : draftRaw
  const normalized = draft?.trim().toLowerCase() ?? null
  const previewColor = normalized !== null && HEX_COLOR_RE.test(normalized) ? normalized : color
  const textColor = readableTextColor(previewColor)
  const ringColor = readableTextColor(previewColor, 0.32)

  useEffect(() => {
    colorRef.current = color
  }, [color])

  const clearTimer = (): void => {
    if (timer.current === null) return
    window.clearTimeout(timer.current)
    timer.current = null
  }

  const commit = (): void => {
    const next = pending.current
    clearTimer()
    pending.current = null
    if (next !== null && next !== colorRef.current) onChange(next)
  }

  function close(): void {
    setOpen(false)
    commit()
    setDraft(null)
  }

  useEffect(() => clearTimer, [])

  // Dragging only updates the local preview; the store is committed after a short idle delay so
  // the CSS variable projection stays smooth.
  const stage = (next: string): void => {
    const hex = next.trim().toLowerCase()
    setDraft(hex)
    pending.current = hex
    clearTimer()
    timer.current = window.setTimeout(commit, COLOR_COMMIT_DELAY_MS)
  }

  return (
    <div className="st-color" ref={ref}>
      {onReset ? (
        <button
          type="button"
          className="st-reset"
          aria-label={`Reset ${ariaLabel}`}
          title="Reset to default"
          onClick={() => {
            clearTimer()
            pending.current = null
            setDraft(null)
            onReset()
          }}
        >
          <ResetGlyph />
        </button>
      ) : null}
      <button
        type="button"
        className="st-color__pill"
        style={{ backgroundColor: previewColor, color: textColor, borderColor: ringColor }}
        aria-label={ariaLabel}
        aria-expanded={open}
        onClick={() => (open ? close() : setOpen(true))}
      >
        <span aria-hidden="true" className="st-color__dot" style={{ borderColor: ringColor }} />
        <span>{previewColor}</span>
      </button>
      {open ? (
        <div className="st-color__pop">
          <input type="color" className="st-color__native" value={previewColor} aria-label={`${ariaLabel} picker`} onChange={(event) => stage(event.target.value)} />
          <input
            type="text"
            className="st-input st-input--hex"
            value={draft ?? color}
            spellCheck={false}
            maxLength={7}
            aria-label={`${ariaLabel} hex value`}
            aria-invalid={draft !== null && !HEX_COLOR_RE.test(draft.trim())}
            onChange={(event) => {
              const next = event.target.value
              setDraft(next)
              if (HEX_COLOR_RE.test(next.trim())) stage(next)
            }}
            onBlur={() => {
              commit()
              setDraft(null)
            }}
          />
        </div>
      ) : null}
    </div>
  )
}

function FontInput({ value, placeholder, ariaLabel, mono = false, onChange }: {
  readonly value: string
  readonly placeholder: string
  readonly ariaLabel: string
  readonly mono?: boolean
  readonly onChange: (next: string) => void
}): JSX.Element {
  const [draft, setDraft] = useState<string | null>(null)
  return (
    <input
      type="text"
      className={`st-input st-input--wide${mono ? ' st-input--mono' : ''}`}
      value={draft ?? value}
      placeholder={placeholder}
      spellCheck={false}
      aria-label={ariaLabel}
      onChange={(event) => {
        setDraft(event.target.value)
        onChange(event.target.value)
      }}
      onBlur={() => setDraft(null)}
    />
  )
}

function ThemeSlider({ value, min = 0, max, suffix = '', valueLabel, onChange, ariaLabel }: {
  readonly value: number
  readonly min?: number
  readonly max: number
  readonly suffix?: string
  readonly valueLabel?: string | undefined
  readonly onChange: (next: number) => void
  readonly ariaLabel: string
}): JSX.Element {
  const fill = Math.max(0, Math.min(100, ((value - min) / (max - min)) * 100))
  return (
    <div className="st-slider">
      <input
        type="range"
        className="theme-slider"
        min={min}
        max={max}
        step={1}
        value={value}
        aria-label={ariaLabel}
        onChange={(event) => onChange(Number(event.target.value))}
        style={{ background: `linear-gradient(to right, var(--accent) 0%, var(--accent) ${fill}%, var(--bar-track) ${fill}%, var(--bar-track) 100%)` }}
      />
      <span className="st-slider__value">{valueLabel ?? `${value}${suffix}`}</span>
    </div>
  )
}

function ImportThemeDialog({ variant, onImport }: { readonly variant: ThemeVariant; readonly onImport: (value: string) => void }): JSX.Element {
  const [open, setOpen] = useState(false)
  const [value, setValue] = useState('')
  const [error, setError] = useState<string | null>(null)

  const submit = (): void => {
    try {
      onImport(value)
      setValue('')
      setError(null)
      setOpen(false)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Unable to import that theme string.')
    }
  }

  return (
    <>
      <button type="button" className="st-text-action" onClick={() => setOpen(true)}>
        Import
      </button>
      {open ? (
        <div className="msm-veil" role="presentation" onClick={() => setOpen(false)}>
          <div className="msm panel" role="dialog" aria-modal="true" aria-label={`Import ${variant} theme`} onClick={(event) => event.stopPropagation()}>
            <div className="msm-head">
              <div>
                <h2 className="msm-title">Import {variant} theme</h2>
                <p className="st-note" style={{ marginTop: 6 }}>
                  Paste a <code>codex-theme-v1:</code> share string. The embedded variant must match {variant}, and the selected code theme must exist for that variant.
                </p>
              </div>
            </div>
            <div className="msm-sec">
              <textarea
                className="st-input st-input--mono"
                style={{ width: '100%', height: 110, padding: 8, resize: 'vertical' }}
                value={value}
                rows={5}
                spellCheck={false}
                aria-label="Theme share string"
                placeholder='codex-theme-v1:{"codeThemeId":"linear",...}'
                onChange={(event) => {
                  setValue(event.target.value)
                  setError(null)
                }}
              />
              {error !== null ? <p className="st-error" style={{ padding: '8px 0 0' }}>{error}</p> : null}
            </div>
            <div className="msm-foot">
              <span className="msm-spacer" />
              <button type="button" className="btn btn--ghost btn--sm" onClick={() => setOpen(false)}>
                Cancel
              </button>
              <button type="button" className="btn btn--primary btn--sm" disabled={value.trim().length === 0} onClick={submit}>
                Import
              </button>
            </div>
          </div>
        </div>
      ) : null}
    </>
  )
}
