import { noteStep } from './diagnostics'

export type View =
  | 'overview'
  | 'providers'
  | 'usage'
  | 'stats'
  | 'logs'
  | 'integrations'
  | 'machines'
  | 'experimental'
  | 'appearance'


export interface ViewEntry {
  readonly view: View
  readonly label: string
  readonly tagline: string
}

const VIEW_HASHES: Record<View, string> = {
  overview: '#/overview',
  providers: '#/providers',
  usage: '#/usage',
  stats: '#/stats',
  logs: '#/logs',
  integrations: '#/integrations',
  machines: '#/machines',
  experimental: '#/experimental',
  appearance: '#/appearance',
}

const VIEW_STORAGE_KEY = 'prism-view'

const VIEW_BY_HASH: Record<string, View> = {
  '#/overview': 'overview',
  '#/providers': 'providers',
  '#/usage': 'usage',
  '#/stats': 'stats',
  '#/logs': 'logs',
  '#/integrations': 'integrations',
  '#/machines': 'machines',
  '#/experimental': 'experimental',
  '#/appearance': 'appearance',
}


export const VIEWS: readonly ViewEntry[] = [
  { view: 'overview', label: 'Overview', tagline: 'Daemon state and default context window' },
  { view: 'usage', label: 'Accounts', tagline: 'Per-account quota, pinning and login' },
  { view: 'stats', label: 'Stats', tagline: 'Request and token statistics' },
  { view: 'logs', label: 'Logs', tagline: 'Model requests and failover history' },
  { view: 'providers', label: 'Providers', tagline: 'Wires, models, credentials' },
  { view: 'integrations', label: 'Integrations', tagline: 'Codex / Grok / OMP apply and rollback' },
  { view: 'machines', label: 'Machines', tagline: 'Remote hosts over SSH' },
  { view: 'experimental', label: 'Experimental', tagline: 'Unfinished features, off by default' },
  { view: 'appearance', label: 'Appearance', tagline: 'Theme, window glass, typography' },
]

export function hashFor(view: View): string {
  return VIEW_HASHES[view]
}

export function viewFromHash(raw: string): View {
  return VIEW_BY_HASH[raw] ?? 'overview'
}

export function readCurrentView(): View {
  if (window.location.hash === '') return storedView() ?? 'overview'
  return viewFromHash(window.location.hash)
}

export function isView(value: unknown): value is View {
  return VIEWS.some((entry) => entry.view === value)
}

export function storedView(): View | null {
  try {
    const value = window.localStorage.getItem(VIEW_STORAGE_KEY)
    return isView(value) ? value : null
  } catch {
    return null
  }
}

export function rememberView(view: View): void {
  try {
    window.localStorage.setItem(VIEW_STORAGE_KEY, view)
  } catch {
    return
  }
}

export function navigateTo(view: View, replace = false): void {
  const next = hashFor(view)
  if (window.location.hash === next) return
  noteStep({ kind: 'navigate', view })
  if (replace) {
    window.history.replaceState(null, '', next)
    window.dispatchEvent(new HashChangeEvent('hashchange'))
  } else {
    window.location.hash = next
  }
}
