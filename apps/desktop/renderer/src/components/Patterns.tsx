import { useEffect, useRef, type KeyboardEvent, type ReactNode } from 'react'
import { createPortal } from 'react-dom'

export type Tone = 'ok' | 'warn' | 'danger' | 'muted' | 'info'

/* Screen title block. `actions` render in a medium control row, so every button, input and
   segmented control placed there shares one height. */
export function PageHeader({
  id,
  icon,
  title,
  actions,
}: {
  readonly id: string
  readonly icon?: string
  readonly title: string
  readonly actions?: ReactNode
}): JSX.Element {
  return (
    <header className="page-head">
      <div className="page-head__titles">
        <h1 id={id}>
          {icon !== undefined ? (
            <svg width="19" height="19" aria-hidden="true"><use href={icon} /></svg>
          ) : null}
          {title}
        </h1>
      </div>
      {actions !== undefined ? <div className="ctl-row page-head__actions">{actions}</div> : null}
    </header>
  )
}

/* The one filters bar of a screen. */
export function Toolbar({
  label,
  children,
}: {
  readonly label: string
  readonly children: ReactNode
}): JSX.Element {
  return (
    <div className="ctl-row toolbar" role="toolbar" aria-label={label}>
      {children}
    </div>
  )
}

export function ToolbarSpacer(): JSX.Element {
  return <span className="ctl-spacer" aria-hidden="true" />
}

/* Titled group of rows. */
export function Section({
  title,
  action,
  children,
}: {
  readonly title: string
  readonly action?: ReactNode
  readonly children: ReactNode
}): JSX.Element {
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

/* Label + helper text on the left, controls on the right. */
export function SettingRow({
  title,
  control,
  stacked = false,
  titleAdornment,
}: {
  readonly title: ReactNode
  readonly control: ReactNode
  readonly stacked?: boolean
  readonly titleAdornment?: ReactNode
}): JSX.Element {
  return (
    <div className={`st-row${stacked ? ' st-row--stack' : ''}`}>
      <div className="st-row__text">
        <div className="st-row__title">
          <h3>{title}</h3>
          {titleAdornment}
        </div>
      </div>
      <div className="st-row__control">{control}</div>
    </div>
  )
}

export function StatusChip({
  tone,
  pulse = false,
  children,
}: {
  readonly tone: Tone
  readonly pulse?: boolean
  readonly children: ReactNode
}): JSX.Element {
  return (
    <span className={`chip chip-${tone}`}>
      <span className={`dot dot-${tone}${pulse ? ' dot-pulse' : ''}`} aria-hidden="true" />
      {children}
    </span>
  )
}

export function Metric({
  label,
  value,
  hint,
  tone = 'default',
}: {
  readonly label: string
  readonly value: string
  readonly hint?: string
  readonly tone?: 'default' | 'ok' | 'warn' | 'error'
}): JSX.Element {
  return (
    <div className={`metric stat--${tone}`}>
      <p className="metric__label">{label}</p>
      <p className="metric__value stat__value">{value}</p>
      {hint !== undefined ? <p className="metric__hint">{hint}</p> : null}
    </div>
  )
}

const FOCUSABLE = 'button:not(:disabled), [href], input:not(:disabled), select:not(:disabled), textarea:not(:disabled), [tabindex]:not([tabindex="-1"])'

/* Shared modal shell for AddAccount, Provider and ModelSettings: portal, veil, Escape,
   focus on open, Tab kept inside, focus returned on close. */
export function Dialog({
  label,
  title,
  subtitle,
  onClose,
  footer,
  error,
  wide = false,
  children,
}: {
  readonly label: string
  readonly title: ReactNode
  readonly subtitle?: ReactNode
  readonly onClose: () => void
  readonly footer: ReactNode
  readonly error?: ReactNode
  readonly wide?: boolean
  readonly children: ReactNode
}): JSX.Element {
  const panelRef = useRef<HTMLElement>(null)

  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null
    const panel = panelRef.current
    if (panel !== null && !panel.contains(document.activeElement)) {
      const first = panel.querySelector<HTMLElement>('.dlg__body input, .dlg__body button, .dlg__body select')
      ;(first ?? panel).focus()
    }
    return () => previous?.focus?.()
  }, [])

  function onKeyDown(event: KeyboardEvent): void {
    if (event.key === 'Escape') {
      event.stopPropagation()
      onClose()
      return
    }
    if (event.key !== 'Tab') return
    const nodes = Array.from(panelRef.current?.querySelectorAll<HTMLElement>(FOCUSABLE) ?? [])
    const first = nodes[0]
    const last = nodes[nodes.length - 1]
    if (first === undefined || last === undefined) return
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault()
      last.focus()
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault()
      first.focus()
    }
  }

  return createPortal(
    <div className="dlg-veil" role="presentation" onClick={onClose}>
      <section
        ref={panelRef}
        className={`dlg${wide ? ' dlg--wide' : ''}`}
        role="dialog"
        aria-modal="true"
        aria-label={label}
        tabIndex={-1}
        onClick={(event) => event.stopPropagation()}
        onKeyDown={onKeyDown}
      >
        <header className="dlg__head">
          <div>
            <div className="dlg__title">{title}</div>
            {subtitle !== undefined ? <div className="dlg__sub">{subtitle}</div> : null}
          </div>
          <button type="button" className="ibtn" aria-label="Close" onClick={onClose}>
            <svg width="13" height="13" viewBox="0 0 14 14" fill="none" stroke="currentColor" strokeWidth="1.2" strokeLinecap="round"><path d="M2.5 2.5l9 9M11.5 2.5l-9 9" /></svg>
          </button>
        </header>
        <div className="dlg__body">{children}</div>
        <footer className="dlg__foot ctl-row">{footer}</footer>
        {error !== undefined ? <div className="dlg__error">{error}</div> : null}
      </section>
    </div>,
    document.body,
  )
}

export function DialogSection({
  label,
  meta,
  children,
}: {
  readonly label?: ReactNode
  readonly meta?: ReactNode
  readonly children: ReactNode
}): JSX.Element {
  return (
    <section className="dlg__sec">
      {label !== undefined ? (
        <div className="dlg__label">
          {label}
          {meta !== undefined ? <span className="dlg__label-meta">{meta}</span> : null}
        </div>
      ) : null}
      {children}
    </section>
  )
}
