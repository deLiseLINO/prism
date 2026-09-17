export type Section = 'Overview' | 'Operate' | 'Configure' | 'System'

export type View =
  | 'overview'
  | 'providers'
  | 'usage'
  | 'stats'
  | 'logs'
  | 'integrations'
  | 'machines'
  | 'experimental'


export interface ViewEntry {
  readonly view: View
  readonly label: string
  readonly tagline: string
  readonly section: Section
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
}


export const VIEWS: readonly ViewEntry[] = [
  { view: 'overview', label: 'Overview', tagline: 'Daemon state and default context window', section: 'Overview' },
  { view: 'usage', label: 'Accounts', tagline: 'Per-account quota, pinning and login', section: 'Operate' },
  { view: 'stats', label: 'Stats', tagline: 'Request and token statistics', section: 'Operate' },
  { view: 'logs', label: 'Logs', tagline: 'Model requests and failover history', section: 'Operate' },
  { view: 'providers', label: 'Providers', tagline: 'Wires, models, credentials', section: 'Configure' },
  { view: 'integrations', label: 'Integrations', tagline: 'Codex / Grok / OMP apply and rollback', section: 'System' },
  { view: 'machines', label: 'Machines', tagline: 'Remote hosts over SSH', section: 'System' },
  { view: 'experimental', label: 'Experimental', tagline: 'Unfinished features, off by default', section: 'System' },
]

export const SECTIONS: readonly Section[] = ['Overview', 'Operate', 'Configure', 'System']

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
  if (replace) {
    window.history.replaceState(null, '', next)
    window.dispatchEvent(new HashChangeEvent('hashchange'))
  } else {
    window.location.hash = next
  }
}
