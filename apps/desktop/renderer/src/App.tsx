import { useEffect, useRef, useState } from 'react'
import type { DaemonStatus } from '@prism/contracts'
import { SkinToggle, ThemeToggle } from './components/Ui'
import { useSkin } from './useSkin'
import { useTheme } from './useTheme'
import { SECTIONS, VIEWS, hashFor, navigateTo, readCurrentView, type View } from './routing'
import { AccountsView } from './views/AccountsView'
import { AuthView } from './views/AuthView'
import { CombosRoutesView } from './views/CombosRoutesView'
import { DaemonView } from './views/DaemonView'
import { IntegrationsView } from './views/IntegrationsView'
import { ModelsView } from './views/ModelsView'
import { OverviewView } from './views/OverviewView'
import { ProvidersView } from './views/ProvidersView'
import { UsagePanel } from './views/UsageView'

function renderView(view: View): JSX.Element {
  switch (view) {
    case 'overview':
      return <OverviewView />
    case 'daemon':
      return <DaemonView />
    case 'auth':
      return <AuthView />
    case 'accounts':
      return <AccountsView />
    case 'providers':
      return <ProvidersView />
    case 'models':
      return <ModelsView />
    case 'combos':
      return <CombosRoutesView />
    case 'usage':
      return <UsagePanel />
    case 'integrations':
      return <IntegrationsView />
  }
}

export function App(): JSX.Element {
  const [view, setView] = useState<View>(readCurrentView())
  const [daemon, setDaemon] = useState<DaemonStatus | null>(null)
  const [daemonUnreachable, setDaemonUnreachable] = useState(false)
  const mainRef = useRef<HTMLElement | null>(null)
  const viewRef = useRef(view)
  const { mode, cycle } = useTheme()
  const { skin, cycle: cycleSkin } = useSkin()

  useEffect(() => {
    const onChange = (): void => {
      const next = readCurrentView()
      if (next === viewRef.current) return
      viewRef.current = next
      setView(next)
      mainRef.current?.focus()
    }
    window.addEventListener('hashchange', onChange)
    return () => window.removeEventListener('hashchange', onChange)
  }, [])

  useEffect(() => {
    if (window.location.hash !== hashFor(readCurrentView())) navigateTo('overview', true)
  }, [])

  useEffect(() => {
    let cancelled = false
    void window.prism.daemon.status().then(
      (status) => {
        if (!cancelled) setDaemon(status)
      },
      () => {
        if (!cancelled) setDaemonUnreachable(true)
      },
    )
    const unsubscribe = window.prism.daemon.onStatus((next) => {
      if (cancelled) return
      setDaemon(next)
      setDaemonUnreachable(false)
    })
    return () => {
      cancelled = true
      unsubscribe()
    }
  }, [])

  const active = VIEWS.find((entry) => entry.view === view) ?? VIEWS[0]!

  return (
    <div className="app">
      <header className="app__topbar">
        <div className="app__brand">
          <p className="app__mark">Prism</p>
          <span className="app__brand-sep" aria-hidden="true" />
          <p className="app__tagline">Local control plane</p>
        </div>
        <div className="app__topbar-actions">
          <ThemeToggle mode={mode} onCycle={cycle} />
          <SkinToggle skin={skin} onCycle={cycleSkin} />
          <DaemonPill status={daemon} unreachable={daemonUnreachable} />
        </div>
      </header>
      <nav className="app__navbar" aria-label="Workflows">
        {SECTIONS.map((section) => (
          <div className="nav-section" key={section}>
            <p className="nav-section__title">{section}</p>
            <div className="nav">
              {VIEWS.filter((entry) => entry.section === section).map((entry) => {
                const isActive = entry.view === view
                return (
                  <button
                    key={entry.view}
                    type="button"
                    className={`nav__item ${isActive ? 'nav__item--active' : ''}`.trim()}
                    aria-current={isActive ? 'page' : undefined}
                    onClick={() => navigateTo(entry.view)}
                  >
                    <span className="nav__label">{entry.label}</span>
                  </button>
                )
              })}
            </div>
          </div>
        ))}
      </nav>
      <main className="app__main" id="main" ref={mainRef} tabIndex={-1}>
        <header className="app__main-header">
          <p className="app__crumb">{active.section}</p>
          <h1 className="app__title">{active.label}</h1>
          <p className="app__subtitle">{active.tagline}</p>
        </header>
        <section className="app__content">{renderView(view)}</section>
      </main>
    </div>
  )
}

function DaemonPill({
  status,
  unreachable,
}: {
  readonly status: DaemonStatus | null
  readonly unreachable: boolean
}): JSX.Element {
  const tone = unreachable
    ? 'error'
    : status === null
      ? 'muted'
      : status.state === 'ready'
        ? 'ok'
        : status.state === 'failed'
          ? 'error'
          : 'warn'
  const label = unreachable
    ? 'Daemon unreachable'
    : status === null
      ? 'Daemon …'
      : `Daemon ${status.state}`
  return (
    <div className="daemon-pill">
      <span className={`daemon-pill__dot daemon-pill__dot--${tone}`} aria-hidden="true" />
      <span className="daemon-pill__label">{label}</span>
      {status?.endpoint !== null && status?.endpoint !== undefined ? (
        <span className="daemon-pill__endpoint">{status.endpoint}</span>
      ) : null}
    </div>
  )
}
