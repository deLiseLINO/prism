import { useEffect, useRef, useState, type CSSProperties } from 'react'
import type { DaemonStatus, UpdaterStatus } from '@prism/contracts'
import { ThemeToggle } from './components/Ui'
import { useTheme } from './useTheme'
import { SECTIONS, VIEWS, hashFor, navigateTo, readCurrentView, type View } from './routing'
import { DaemonView } from './views/DaemonView'
import { IntegrationsView } from './views/IntegrationsView'
import { LogsView } from './views/LogsView'
import { OverviewView } from './views/OverviewView'
import { MachinesView } from './views/MachinesView'
import { ProvidersView } from './views/ProvidersView'
import { StatsView } from './views/StatsView'
import { UpdateView } from './views/UpdateView'
import { UsagePanel } from './views/UsageView'

const VIEW_ICONS: Record<View, string> = {
  overview: '#i-gauge',
  usage: '#i-heart',
  stats: '#i-usage',
  providers: '#i-plug',
  daemon: '#i-daemon',
  integrations: '#i-puzzle',
  machines: '#i-cube',
  logs: '#i-logs',
  update: '#i-refresh',

}
function IconSprite(): JSX.Element {
  return (
    <svg width="0" height="0" style={{ position: 'absolute' }} aria-hidden="true">
      <defs>
        <symbol id="i-prism" viewBox="0 0 20 20"><path d="M10 2 18.5 17H1.5L10 2Z" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinejoin="round"/><path d="M10 2v15M6.3 12.4 10 17l3.7-4.6" fill="none" stroke="currentColor" strokeWidth="1.2" opacity="0.55"/></symbol>
        <symbol id="i-gauge" viewBox="0 0 20 20"><path d="M3 15a7.5 7.5 0 1 1 14 0" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round"/><path d="M10 15 13.8 8.5" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round"/></symbol>
        <symbol id="i-heart" viewBox="0 0 20 20"><path d="M10 16.5S3 12.4 3 7.9C3 5.2 5.2 3.5 7.4 3.5c1 0 2 .4 2.6 1.2C10.6 3.9 11.6 3.5 12.6 3.5 14.8 3.5 17 5.2 17 7.9c0 4.5-7 8.6-7 8.6Z" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinejoin="round"/></symbol>
        <symbol id="i-plug" viewBox="0 0 20 20"><path d="M6.5 2.5v4M13.5 2.5v4M4.5 6.5h11v3.2c0 3-2.5 5.3-5.5 5.3s-5.5-2.3-5.5-5.3V6.5ZM10 15v2.5" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round"/></symbol>
        <symbol id="i-daemon" viewBox="0 0 20 20"><path d="M4.5 8.2a5.5 5.5 0 0 1 10.4-1.6A4.3 4.3 0 0 1 15.5 15h-11a4 4 0 0 1 0-8.1" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round"/><path d="M10 12.5v3M10 15.5l2.2-2.2M10 15.5l-2.2-2.2" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"/></symbol>
        <symbol id="i-puzzle" viewBox="0 0 20 20"><path d="M7.3 3.5a1.7 1.7 0 1 1 3.4 0V5h2.8a1 1 0 0 1 1 1v2.8h1.5a1.7 1.7 0 1 1 0 3.4H14.5v2.8a1 1 0 0 1-1 1h-3v-1.6a1.7 1.7 0 1 0-3.4 0v1.6h-3a1 1 0 0 1-1-1V6a1 1 0 0 1 1-1h2.2V3.5Z" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinejoin="round"/></symbol>
        <symbol id="i-sun" viewBox="0 0 20 20"><circle cx="10" cy="10" r="3.6" fill="none" stroke="currentColor" strokeWidth="1.6"/><path d="M10 1.8V4M10 16v2.2M18.2 10H16M4 10H1.8M15.7 4.3l-1.6 1.6M5.9 14.1l-1.6 1.6M15.7 15.7l-1.6-1.6M5.9 5.9 4.3 4.3" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round"/></symbol>
        <symbol id="i-moon" viewBox="0 0 20 20"><path d="M15.5 12.6A6.8 6.8 0 0 1 7.4 4.5a6.8 6.8 0 1 0 8.1 8.1Z" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinejoin="round"/></symbol>
        <symbol id="i-refresh" viewBox="0 0 20 20"><path d="M16 8.5A6.5 6.5 0 1 0 15.4 13" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round"/><path d="M16.2 3.6v5h-5" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round"/></symbol>
        <symbol id="i-logs" viewBox="0 0 20 20"><path d="M4 3.5h12v13H4z" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinejoin="round"/><path d="M7 7h6M7 10h6M7 13h4" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round"/></symbol>
      </defs>
    </svg>
  )
}

function renderView(view: View): JSX.Element {
  switch (view) {
    case 'overview':
      return <OverviewView />
    case 'daemon':
      return <DaemonView />
    case 'providers':
      return <ProvidersView />
    case 'usage':
      return <UsagePanel />
    case 'stats':
      return <StatsView />
    case 'logs':
      return <LogsView />
    case 'integrations':
      return <IntegrationsView />
    case 'machines':
      return <MachinesView />
    case 'update':
      return <UpdateView />

  }
}

export function App(): JSX.Element {
  const [view, setView] = useState<View>(readCurrentView())
  const [daemon, setDaemon] = useState<DaemonStatus | null>(null)
  const [daemonUnreachable, setDaemonUnreachable] = useState(false)
  const [updater, setUpdater] = useState<UpdaterStatus | null>(null)
  const mainRef = useRef<HTMLElement | null>(null)
  const viewRef = useRef(view)
  const { mode, cycle } = useTheme()

  useEffect(() => {
    const onChange = (): void => {
      const next = readCurrentView()
      if (next === viewRef.current) return
      viewRef.current = next
      setView(next)
      mainRef.current?.focus({ preventScroll: true })
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

  useEffect(() => {
    const unsubscribe = window.prism.updater.onStatus(setUpdater)
    void window.prism.updater.status().then(setUpdater, () => {})
    return unsubscribe
  }, [])

  return (
    <>
      <IconSprite />
      <div className="drag-region" aria-hidden="true" />
      <aside className="rail" aria-label="Primary">
        <div className="brand">
          <span className="brand-mark"><svg width="22" height="22"><use href="#i-prism" /></svg></span>
          <span className="brand-name">Prism</span>
        </div>
        <div className="brand-sub">model router control</div>
        <nav className="nav" aria-label="Workflows">
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
                      className="nav-btn"
                      aria-current={isActive ? 'page' : undefined}
                      onClick={() => navigateTo(entry.view)}
                    >
                      <svg width="16" height="16" style={{ marginRight: 9, flex: 'none' } as CSSProperties}><use href={VIEW_ICONS[entry.view]} /></svg>
                      <span className="nav__label">{entry.label}</span>
                    </button>
                  )
                })}
              </div>
            </div>
          ))}
        </nav>
        <div className="rail-foot">
          <DaemonMini status={daemon} unreachable={daemonUnreachable} />
          <div className="rail-toggles">
            <ThemeToggle mode={mode} onCycle={cycle} />
          </div>
        </div>
      </aside>
      <main className="app" id="main" ref={mainRef} tabIndex={-1}>
        <div className="wrap">
          {updater !== null && updater.state === 'downloaded' ? (
            <div className="banner banner--ok" role="status" style={{ marginBottom: 14 }}>
              <div>
                <p className="banner__title">Update ready</p>
                <p className="banner__detail">Prism {updater.downloadedVersion} downloaded — restart to install.</p>
              </div>
              <div className="banner__action">
                <button type="button" className="btn btn--primary" onClick={() => void window.prism.updater.install()}>
                  Restart to update
                </button>
              </div>
            </div>
          ) : null}
          {renderView(view)}
        </div>
      </main>
    </>
  )
}

function DaemonMini({
  status,
  unreachable,
}: {
  readonly status: DaemonStatus | null
  readonly unreachable: boolean
}): JSX.Element {
  const dotClass = unreachable
    ? 'dot dot-danger'
    : status === null
      ? 'dot dot-muted'
      : status.state === 'ready'
        ? 'dot dot-ok dot-pulse'
        : status.state === 'failed'
          ? 'dot dot-danger'
          : 'dot dot-warn'
  const label = unreachable
    ? 'daemon unreachable'
    : status === null
      ? 'daemon …'
      : `daemon ${status.state}`
  return (
    <div className="daemon-mini">
      <span className={dotClass} aria-hidden="true" />
      <span>{label}</span>
      {status?.pid !== null && status?.pid !== undefined ? <span className="num" style={{ marginLeft: 'auto' }}>pid {status.pid}</span> : null}
    </div>
  )
}
