import { Fragment, useEffect, useRef, useState, type CSSProperties } from 'react'

import type { DaemonStatus, UpdaterStatus } from '@prism/contracts'
import { ActiveHostProvider, useActiveHost } from './ActiveHost'
import { AppearanceProvider } from './useAppearance'
import { ExperimentalFlagsProvider, useExperimentalFlags } from './experimental'
import { VIEWS, hashFor, navigateTo, readCurrentView, rememberView, type View, type ViewEntry } from './routing'

import { IntegrationsView } from './views/IntegrationsView'
import { LogsView } from './views/LogsView'
import { OverviewView } from './views/OverviewView'
import { MachinesView } from './views/MachinesView'
import { ProvidersView } from './views/ProvidersView'
import { StatsView } from './views/StatsView'
import { UsagePanel } from './views/UsageView'
import { UpdateMini } from './components/UpdateMini'
import { SIDEBAR_KEY, historyAvailability, initialSidebarOpen, viewForShortcut, type NavigationLike } from './shell'
import { bridge } from './bridge'
import { ExperimentalView } from './views/ExperimentalView'
import { AppearanceView } from './views/AppearanceView'
import { BootScreen, useDaemonBoot } from './boot'

const VIEW_ICONS: Record<View, string> = {
  overview: '#i-home',
  usage: '#i-heart',
  stats: '#i-usage',
  providers: '#i-plug',
  integrations: '#i-puzzle',
  machines: '#i-cube',
  logs: '#i-logs',
  experimental: '#i-flask',
  appearance: '#i-sliders',
}

function IconSprite(): JSX.Element {
  return (
    <svg width="0" height="0" style={{ position: 'absolute' }} aria-hidden="true">
      <defs>
        <symbol id="i-prism" viewBox="0 0 20 20"><path d="M10 2 18.5 17H1.5L10 2Z" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinejoin="round"/><path d="M10 2v15M6.3 12.4 10 17l3.7-4.6" fill="none" stroke="currentColor" strokeWidth="1.2" opacity="0.55"/></symbol>
        <symbol id="i-sidebar" viewBox="0 0 20 20"><rect x="2.5" y="3.5" width="15" height="13" rx="3" fill="none" stroke="currentColor" strokeWidth="1.5"/><path d="M8 3.5v13" stroke="currentColor" strokeWidth="1.5"/></symbol>
        <symbol id="i-back" viewBox="0 0 20 20"><path d="M16 10H4.5M9.5 4.5 4 10l5.5 5.5" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round"/></symbol>
        <symbol id="i-forward" viewBox="0 0 20 20"><path d="M4 10h11.5M10.5 4.5 16 10l-5.5 5.5" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round"/></symbol>
        <symbol id="i-home" viewBox="0 0 20 20"><path d="M3 9.2 10 3l7 6.2V16a1 1 0 0 1-1 1h-3.5v-4.5h-5V17H4a1 1 0 0 1-1-1V9.2Z" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinejoin="round"/></symbol>
        <symbol id="i-sliders" viewBox="0 0 20 20"><path d="M3.5 6h8M15 6h1.5M3.5 14H5M8.5 14h8" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round"/><circle cx="13" cy="6" r="1.9" fill="none" stroke="currentColor" strokeWidth="1.6"/><circle cx="6.8" cy="14" r="1.9" fill="none" stroke="currentColor" strokeWidth="1.6"/></symbol>
        <symbol id="i-gauge" viewBox="0 0 20 20"><path d="M3 15a7.5 7.5 0 1 1 14 0" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round"/><path d="M10 15 13.8 8.5" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round"/></symbol>
        <symbol id="i-eye" viewBox="0 0 20 20"><path d="M2.5 10S5.8 4.5 10 4.5 17.5 10 17.5 10 14.2 15.5 10 15.5 2.5 10 2.5 10Z" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinejoin="round"/><circle cx="10" cy="10" r="2.6" fill="none" stroke="currentColor" strokeWidth="1.6"/></symbol>
        <symbol id="i-heart" viewBox="0 0 20 20"><path d="M10 16.5S3 12.4 3 7.9C3 5.2 5.2 3.5 7.4 3.5c1 0 2 .4 2.6 1.2C10.6 3.9 11.6 3.5 12.6 3.5 14.8 3.5 17 5.2 17 7.9c0 4.5-7 8.6-7 8.6Z" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinejoin="round"/></symbol>
        <symbol id="i-plug" viewBox="0 0 20 20"><path d="M6.5 2.5v4M13.5 2.5v4M4.5 6.5h11v3.2c0 3-2.5 5.3-5.5 5.3s-5.5-2.3-5.5-5.3V6.5ZM10 15v2.5" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round"/></symbol>
        <symbol id="i-daemon" viewBox="0 0 20 20"><path d="M4.5 8.2a5.5 5.5 0 0 1 10.4-1.6A4.3 4.3 0 0 1 15.5 15h-11a4 4 0 0 1 0-8.1" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round"/><path d="M10 12.5v3M10 15.5l2.2-2.2M10 15.5l-2.2-2.2" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"/></symbol>
        <symbol id="i-puzzle" viewBox="0 0 20 20"><path d="M7.3 3.5a1.7 1.7 0 1 1 3.4 0V5h2.8a1 1 0 0 1 1 1v2.8h1.5a1.7 1.7 0 1 1 0 3.4H14.5v2.8a1 1 0 0 1-1 1h-3v-1.6a1.7 1.7 0 1 0-3.4 0v1.6h-3a1 1 0 0 1-1-1V6a1 1 0 0 1 1-1h2.2V3.5Z" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinejoin="round"/></symbol>
        <symbol id="i-sun" viewBox="0 0 20 20"><circle cx="10" cy="10" r="3.6" fill="none" stroke="currentColor" strokeWidth="1.6"/><path d="M10 1.8V4M10 16v2.2M18.2 10H16M4 10H1.8M15.7 4.3l-1.6 1.6M5.9 14.1l-1.6 1.6M15.7 15.7l-1.6-1.6M5.9 5.9 4.3 4.3" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round"/></symbol>
        <symbol id="i-moon" viewBox="0 0 20 20"><path d="M15.5 12.6A6.8 6.8 0 0 1 7.4 4.5a6.8 6.8 0 1 0 8.1 8.1Z" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinejoin="round"/></symbol>
        <symbol id="i-usage" viewBox="0 0 20 20"><path d="M3.5 4.5v11M10 2.5v13M16.5 6.5v9" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round"/></symbol>
        <symbol id="i-cube" viewBox="0 0 20 20"><path d="M10 2.2 17 6v8L10 18 3 14V6l7-3.8Z" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinejoin="round"/><path d="M3 6l7 3.8L17 6M10 9.8V18" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinejoin="round"/></symbol>
        <symbol id="i-flask" viewBox="0 0 20 20"><path d="M8 2.5h4M9.5 2.5v5.2L4.6 15a2.2 2.2 0 0 0 1.9 3.4h7a2.2 2.2 0 0 0 1.9-3.4L10.5 7.7V2.5" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round"/><path d="M7 13h6" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round"/></symbol>
        <symbol id="i-logs" viewBox="0 0 20 20"><path d="M4 3.5h12v13H4z" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinejoin="round"/><path d="M7 7h6M7 10h6M7 13h4" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round"/></symbol>
      </defs>
    </svg>
  )
}

function renderView(
  view: View,
  daemon: DaemonStatus | null,
  daemonUnreachable: boolean,
): JSX.Element {
  switch (view) {
    case 'overview':
      return <OverviewView daemon={daemon} daemonUnreachable={daemonUnreachable} />
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
    case 'experimental':
      return <ExperimentalView />
    case 'appearance':
      return <AppearanceView />

  }
}

export function App(): JSX.Element {
  return (
    <AppearanceProvider>
      <ExperimentalFlagsProvider>
        <ActiveHostProvider>
          <AppShell />
        </ActiveHostProvider>
      </ExperimentalFlagsProvider>
    </AppearanceProvider>
  )
}

function AppShell(): JSX.Element {
  const [view, setView] = useState<View>(readCurrentView())
  const { flags } = useExperimentalFlags()
  const machinesVisible = flags.remoteInstall
  const [daemon, setDaemon] = useState<DaemonStatus | null>(null)
  const [daemonUnreachable, setDaemonUnreachable] = useState(false)
  const [updater, setUpdater] = useState<UpdaterStatus | null>(null)
  useEffect(() => {
    const version = updater?.currentVersion ?? ''
    const installedRc = /-rc\.[0-9]+$/.test(version)
    if (version === '' && !flags.rcChannel) return
    void bridge.updater.setRcChannel(flags.rcChannel || installedRc)
  }, [flags.rcChannel, updater?.currentVersion])
  const [sidebarOpen, setSidebarOpen] = useState(() => {
    let stored: string | null = null
    try {
      stored = window.localStorage.getItem(SIDEBAR_KEY)
    } catch {
      stored = null
    }
    return initialSidebarOpen(stored)
  })
  const [history, setHistory] = useState(() => historyAvailability(navigationApi()))
  const mainRef = useRef<HTMLElement | null>(null)
  const viewRef = useRef(view)
  const boot = useDaemonBoot(daemon, daemonUnreachable)

  useEffect(() => {
    const apply = (): void => {
      const factor = bridge.zoom.factor()
      document.documentElement.style.setProperty('--zoom', String(factor))
    }
    apply()
    window.addEventListener('resize', apply)
    return () => window.removeEventListener('resize', apply)
  }, [])
  const { host } = useActiveHost()

  useEffect(() => {
    if (!machinesVisible && view === 'machines') navigateTo('overview', true)
  }, [machinesVisible, view])

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
    rememberView(view)
  }, [view])

  useEffect(() => {
    let cancelled = false
    void bridge.daemon.status().then(
      (status) => {
        if (!cancelled) setDaemon(status)
      },
      () => {
        if (!cancelled) setDaemonUnreachable(true)
      },
    )
    const unsubscribe = bridge.daemon.onStatus((next) => {
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
    const unsubscribe = bridge.updater.onStatus(setUpdater)
    void bridge.updater.status().then(setUpdater, () => {})
    return unsubscribe
  }, [])

  const visibleViews = VIEWS.filter((entry) => entry.view !== 'machines' || machinesVisible)
  const visibleIds = visibleViews.map((entry) => entry.view)
  const mainViews = visibleViews.filter((entry) => entry.view !== 'appearance')
  const appearanceEntry = visibleViews.find((entry) => entry.view === 'appearance')

  useEffect(() => {
    try {
      window.localStorage.setItem(SIDEBAR_KEY, sidebarOpen ? 'open' : 'closed')
    } catch {
      return
    }
  }, [sidebarOpen])

  useEffect(() => {
    const refresh = (): void => setHistory(historyAvailability(navigationApi()))
    window.addEventListener('hashchange', refresh)
    const nav = navigationApi() as (NavigationLike & EventTarget) | undefined
    nav?.addEventListener('currententrychange', refresh)
    return () => {
      window.removeEventListener('hashchange', refresh)
      nav?.removeEventListener('currententrychange', refresh)
    }
  }, [])

  useEffect(() => {
    const onKey = (event: KeyboardEvent): void => {
      if (!(event.metaKey || event.ctrlKey) || event.altKey || event.shiftKey) return
      if (event.key === 'b') {
        event.preventDefault()
        setSidebarOpen((open) => !open)
      } else if (event.key === '[') {
        event.preventDefault()
        window.history.back()
      } else if (event.key === ']') {
        event.preventDefault()
        window.history.forward()
      } else {
        const target = viewForShortcut(event.key, visibleIds)
        if (target === null) return
        event.preventDefault()
        navigateTo(target)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  })

  const daemonPill = daemonPillState(daemon, daemonUnreachable)

  const daemonLabel = daemonPill.label
  const pid = daemon?.pid
  const daemonTitle = pid !== null && pid !== undefined ? `Daemon ${daemonLabel.toLowerCase()} (pid ${pid})` : `Daemon ${daemonLabel.toLowerCase()}`

  return (
    <div className="shell" data-sidebar={sidebarOpen ? 'open' : 'closed'}>
      <IconSprite />
      <header className="topbar">
        <div className="topbar-left">
          <button
            type="button"
            className="ghost-btn sidebar-toggle"
            aria-label="Toggle sidebar"
            title="Expand or collapse the sidebar (Ctrl/Cmd+B)"
            aria-pressed={sidebarOpen}
            onClick={() => setSidebarOpen((open) => !open)}
          >
            <svg width="20" height="20" aria-hidden="true"><use href="#i-sidebar" /></svg>
          </button>
          <button type="button" className="ghost-btn" aria-label="Back" title="Back (Ctrl/Cmd+[)" disabled={!history.back} onClick={() => window.history.back()}>
            <svg width="20" height="20" aria-hidden="true"><use href="#i-back" /></svg>
          </button>
          <button type="button" className="ghost-btn" aria-label="Forward" title="Forward (Ctrl/Cmd+])" disabled={!history.forward} onClick={() => window.history.forward()}>
            <svg width="20" height="20" aria-hidden="true"><use href="#i-forward" /></svg>
          </button>
        </div>
      </header>
      <aside className="rail" aria-label="Primary">
        <nav className="rail-nav" aria-label="Workflows">
          {mainViews.map((entry) => (
            <RailButton key={entry.view} entry={entry} active={entry.view === view} />
          ))}
        </nav>
        <div className="rail-foot">
          <button
            type="button"
            className={`rail-btn rail-btn--status rail-btn--${daemonPill.tone}`}
            aria-label={`${daemonTitle}. Open overview.`}
            title={daemonTitle}
            onClick={() => navigateTo('overview')}
          >
            <span className="status-ring"><span className={daemonPill.dot} aria-hidden="true" /></span>
            <span className="rail-label">{daemonTitle}</span>
          </button>
          <UpdateMini status={updater} />
          {appearanceEntry === undefined ? null : <RailButton entry={appearanceEntry} active={view === 'appearance'} />}
        </div>
      </aside>
      <main className="app" id="main" ref={mainRef} tabIndex={-1}>
        <div className="wrap">
          {boot.kind === 'booting' && host === 'local' ? (
            <BootScreen phase={boot} />
          ) : (
            <>
              <RemoteBanner />
              <Fragment key={host}>
                {renderView(view, daemon, daemonUnreachable)}
              </Fragment>
            </>
          )}
        </div>
      </main>
    </div>
  )
}


function RailButton({ entry, active }: { readonly entry: ViewEntry; readonly active: boolean }): JSX.Element {
  return (
    <button
      type="button"
      className="rail-btn"
      aria-label={entry.label}
      title={entry.label}
      aria-current={active ? 'page' : undefined}
      onClick={() => navigateTo(entry.view)}
    >
      <svg width="22" height="22" aria-hidden="true"><use href={VIEW_ICONS[entry.view]} /></svg>
      <span className="rail-label">{entry.label}</span>
    </button>
  )
}

function navigationApi(): NavigationLike | undefined {
  return (window as Window & { navigation?: NavigationLike }).navigation
}

function daemonPillState(
  status: DaemonStatus | null,
  unreachable: boolean,
): { readonly label: string; readonly tone: 'ok' | 'warn' | 'danger' | 'muted'; readonly dot: string } {
  if (unreachable) return { label: 'Unreachable', tone: 'danger', dot: 'dot dot-danger' }
  if (status === null) return { label: 'Starting', tone: 'muted', dot: 'dot dot-muted' }
  if (status.state === 'ready') return { label: 'Ready', tone: 'ok', dot: 'dot dot-ok dot-pulse' }
  if (status.state === 'failed') return { label: 'Failed', tone: 'danger', dot: 'dot dot-danger' }
  return { label: 'Starting', tone: 'warn', dot: 'dot dot-warn' }
}

function RemoteBanner(): JSX.Element {
  const { host, setHost } = useActiveHost()

  if (host === 'local') return <></>
  return (
    <div className="banner" role="status" style={{ marginBottom: 14 }}>
      <div>
        <p className="banner__title">Managing {host}</p>
        <p className="banner__detail">Views and actions go to that machine's daemon over SSH.</p>
      </div>
      <div className="banner__action">
        <button type="button" className="btn" onClick={() => setHost('local')}>Back to this machine</button>
      </div>
    </div>
  )
}
