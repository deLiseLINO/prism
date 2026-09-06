import type { ThemeMode } from '../useTheme'
import { cloneElement, isValidElement, useEffect, useId, type ReactElement, type ReactNode } from 'react'

export interface CardProps {
  readonly title?: string
  readonly description?: string
  readonly tone?: 'default' | 'warn' | 'error' | 'ok'
  readonly action?: ReactNode
  readonly children: ReactNode
}

export function Card({ title, description, tone = 'default', action, children }: CardProps): JSX.Element {
  const toneClass = tone === 'default' ? '' : `card--${tone}`
  return (
    <section className={`card ${toneClass}`.trim()}>
      {title !== undefined || action !== undefined ? (
        <div className="card__head">
          <div className="card__headings">
            {title !== undefined ? <h3 className="card__title">{title}</h3> : null}
            {description !== undefined ? <p className="card__description">{description}</p> : null}
          </div>
          {action !== undefined ? <div className="card__action">{action}</div> : null}
        </div>
      ) : description !== undefined ? (
        <p className="card__description">{description}</p>
      ) : null}
      <div className="card__body">{children}</div>
    </section>
  )
}

export interface StateProps<T> {
  readonly state:
    | { readonly kind: 'idle' }
    | { readonly kind: 'loading' }
    | { readonly kind: 'ready'; readonly value: T }
    | { readonly kind: 'error'; readonly error: Error }
  readonly children: (value: T) => ReactNode
  readonly loadingLabel?: string
  readonly empty?: ReactNode
  readonly onRetry?: () => void
}

export function AsyncBoundary<T>({
  state,
  children,
  loadingLabel,
  empty,
  onRetry,
}: StateProps<T>): JSX.Element {
  switch (state.kind) {
    case 'idle':
    case 'loading':
      return (
        <div className="state state--loading" role="status" aria-busy="true">
          <span>{loadingLabel ?? 'Loading…'}</span>
          <span className="state__skeleton" style={{ width: '70%' }} aria-hidden="true" />
          <span className="state__skeleton" style={{ width: '45%' }} aria-hidden="true" />
        </div>
      )
    case 'error':
      return (
        <div className="state state--error" role="alert">
          <p className="state__headline">Couldn’t load this section</p>
          <p className="state__detail">{state.error.message}</p>
          {onRetry !== undefined ? (
            <div className="state__retry">
              <Button tone="ghost" size="sm" onClick={onRetry}>
                Retry
              </Button>
            </div>
          ) : null}
        </div>
      )
    case 'ready': {
      const node = children(state.value)
      if (empty !== undefined && isEmpty(node)) {
        return <div className="state state--empty">{empty}</div>
      }
      return <>{node}</>
    }
  }
}

function isEmpty(node: ReactNode): boolean {
  if (node === null || node === undefined || node === false) return true
  if (Array.isArray(node)) return node.length === 0
  return false
}

export interface BannerProps {
  readonly tone: 'info' | 'warn' | 'error' | 'ok'
  readonly title: string
  readonly children?: ReactNode
  readonly action?: ReactNode
}

export function Banner({ tone, title, action, children }: BannerProps): JSX.Element {
  return (
    <div className={`banner banner--${tone}`} role={tone === 'error' ? 'alert' : 'status'}>
      <div className="banner__body">
        <p className="banner__title">{title}</p>
        {children !== undefined ? <p className="banner__detail">{children}</p> : null}
      </div>
      {action !== undefined ? <div className="banner__action">{action}</div> : null}
    </div>
  )
}

export interface ButtonProps {
  readonly children: ReactNode
  readonly onClick: () => void
  readonly disabled?: boolean
  readonly tone?: 'primary' | 'ghost' | 'danger'
  readonly size?: 'md' | 'sm'
  readonly type?: 'button' | 'submit'
  readonly busy?: boolean
  readonly title?: string
  readonly autoFocus?: boolean
}

export function Button({
  children,
  onClick,
  disabled,
  tone = 'primary',
  size = 'md',
  type = 'button',
  busy,
  title,
  autoFocus,
}: ButtonProps): JSX.Element {
  return (
    <button
      type={type}
      className={`btn btn--${tone} btn--${size}`}
      onClick={onClick}
      disabled={disabled === true || busy === true}
      aria-busy={busy === true}
      title={title}
      autoFocus={autoFocus === true}
    >
      {busy === true ? <span className="btn__spinner" aria-hidden="true" /> : null}
      <span>{children}</span>
    </button>
  )
}

export interface ToggleProps {
  readonly checked: boolean
  readonly onChange: (next: boolean) => void
  readonly label: string
  readonly disabled?: boolean
  readonly visuallyHidden?: boolean
}

export function Toggle({ checked, onChange, label, disabled, visuallyHidden }: ToggleProps): JSX.Element {
  return (
    <label className={`toggle ${disabled === true ? 'toggle--disabled' : ''} ${visuallyHidden === true ? 'toggle--icon-only' : ''}`.trim()}>
      <input
        type="checkbox"
        checked={checked}
        disabled={disabled === true}
        onChange={(event) => onChange(event.target.checked)}
      />
      <span className="toggle__track" aria-hidden="true">
        <span className="toggle__thumb" />
      </span>
      <span className={visuallyHidden === true ? 'sr-only' : 'toggle__label'}>{label}</span>
    </label>
  )
}

export interface FieldProps {
  readonly label: string
  readonly hint?: string
  readonly htmlFor: string
  readonly children: ReactNode
}

export function Field({ label, hint, htmlFor, children }: FieldProps): JSX.Element {
  const hintId = `${htmlFor}-hint`
  const linked =
    hint !== undefined && isValidElement(children)
      ? cloneElement(children as ReactElement<Record<string, unknown>>, { 'aria-describedby': hintId })
      : children
  return (
    <div className="field">
      <label className="field__label" htmlFor={htmlFor}>
        {label}
      </label>
      {linked}
      {hint !== undefined ? <p className="field__hint" id={hintId}>{hint}</p> : null}
    </div>
  )
}

export interface TextInputProps {
  readonly id: string
  readonly value: string
  readonly onChange: (next: string) => void
  readonly placeholder?: string
  readonly type?: 'text' | 'password' | 'number'
  readonly autoComplete?: string
  readonly disabled?: boolean
  readonly min?: number
  readonly max?: number
  readonly step?: number
  readonly spellCheck?: boolean
  readonly ariaLabel?: string
}

export function TextInput({
  id,
  value,
  onChange,
  placeholder,
  type = 'text',
  autoComplete,
  disabled,
  min,
  max,
  step,
  spellCheck,
  ariaLabel,
}: TextInputProps): JSX.Element {
  return (
    <input
      id={id}
      type={type}
      className="text-input"
      value={value}
      placeholder={placeholder}
      autoComplete={autoComplete}
      disabled={disabled === true}
      min={min}
      max={max}
      step={step}
      spellCheck={spellCheck}
      aria-label={ariaLabel}
      onChange={(event) => onChange(event.target.value)}
    />
  )
}

export interface SelectProps<T extends string> {
  readonly id: string
  readonly value: T
  readonly onChange: (next: T) => void
  readonly options: readonly { readonly value: T; readonly label: string }[]
  readonly disabled?: boolean
}

export function Select<T extends string>({ id, value, onChange, options, disabled }: SelectProps<T>): JSX.Element {
  return (
    <select
      id={id}
      className="select"
      value={value}
      disabled={disabled === true}
      onChange={(event) => onChange(event.target.value as T)}
    >
      {options.map((option) => (
        <option key={option.value} value={option.value}>
          {option.label}
        </option>
      ))}
    </select>
  )
}

export interface RowProps {
  readonly children: ReactNode
  readonly align?: 'start' | 'end' | 'center'
  readonly gap?: 'tight' | 'normal' | 'loose'
}

export function Row({ children, align = 'start', gap = 'normal' }: RowProps): JSX.Element {
  return (
    <div className={`row row--${align} row--${gap}`}>{children}</div>
  )
}

export interface StackProps {
  readonly children: ReactNode
  readonly gap?: 'tight' | 'normal' | 'loose'
}

export function Stack({ children, gap = 'normal' }: StackProps): JSX.Element {
  return <div className={`stack stack--${gap}`}>{children}</div>
}

export interface SearchInputProps {
  readonly id: string
  readonly value: string
  readonly onChange: (next: string) => void
  readonly placeholder?: string
  readonly ariaLabel?: string
}

export function SearchInput({ id, value, onChange, placeholder, ariaLabel }: SearchInputProps): JSX.Element {
  return (
    <div className="search">
      <span className="search__icon" aria-hidden="true">/</span>
      <input
        id={id}
        type="search"
        className="text-input search__input"
        value={value}
        placeholder={placeholder}
        aria-label={ariaLabel ?? placeholder ?? 'Search'}
        autoComplete="off"
        spellCheck={false}
        onChange={(event) => onChange(event.target.value)}
      />
    </div>
  )
}

export interface StatProps {
  readonly label: string
  readonly value: string
  readonly tone?: 'default' | 'ok' | 'warn' | 'error'
  readonly hint?: string
}

export function Stat({ label, value, tone = 'default', hint }: StatProps): JSX.Element {
  return (
    <div className={`stat stat--${tone}`}>
      <p className="stat__label">{label}</p>
      <p className="stat__value">{value}</p>
      {hint !== undefined ? <p className="stat__hint">{hint}</p> : null}
    </div>
  )
}

export interface EmptyProps {
  readonly title: string
  readonly children?: ReactNode
  readonly action?: ReactNode
}

export function Empty({ title, children, action }: EmptyProps): JSX.Element {
  return (
    <div className="empty">
      <p className="empty__title">{title}</p>
      {children !== undefined ? <div className="empty__body">{children}</div> : null}
      {action !== undefined ? <div className="empty__action">{action}</div> : null}
    </div>
  )
}

export interface ConfirmProps {
  readonly title: string
  readonly detail: string
  readonly confirmLabel: string
  readonly onConfirm: () => void
  readonly onCancel: () => void
  readonly busy?: boolean
}

export function Confirm({ title, detail, confirmLabel, onConfirm, onCancel, busy }: ConfirmProps): JSX.Element {
  const detailId = useId()
  useEffect(() => {
    const onKey = (event: KeyboardEvent): void => {
      if (event.key === 'Escape') onCancel()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onCancel])
  return (
    <div
      className="confirm"
      role="alertdialog"
      aria-modal="false"
      aria-label={title}
      aria-describedby={detailId}
    >
      <p className="confirm__title">{title}</p>
      <p className="confirm__detail" id={detailId}>{detail}</p>
      <div className="confirm__actions">
        <Button tone="ghost" size="sm" autoFocus onClick={onCancel} disabled={busy === true}>
          Cancel
        </Button>
        <Button tone="danger" size="sm" onClick={onConfirm} busy={busy === true}>
          {confirmLabel}
        </Button>
      </div>
    </div>
  )
}

export interface ThemeToggleProps {
  readonly mode: ThemeMode
  readonly onCycle: () => void
}

export function ThemeToggle({ mode, onCycle }: ThemeToggleProps): JSX.Element {
  return (
    <button
      type="button"
      className="theme-toggle"
      onClick={onCycle}
      aria-label={`Theme: ${mode}. Switch theme.`}
      title={`Theme: ${mode} (cycles auto, dark, light)`}
    >
      <span className="theme-toggle__icon" aria-hidden="true">
        {mode === 'auto' ? (
          <svg width="15" height="15"><use href="#i-prism" /></svg>
        ) : (
          <svg width="15" height="15" className={mode === 'dark' ? 'icon-moon' : 'icon-sun'}>
            <use href={mode === 'dark' ? '#i-moon' : '#i-sun'} />
          </svg>
        )}
      </span>
      <span className="theme-toggle__mode">{mode}</span>
    </button>
  )
}
