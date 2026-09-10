export type Section = 'Overview' | 'Operate' | 'Configure' | 'System'

export type View =
  | 'overview'
  | 'daemon'
  | 'providers'
  | 'usage'
  | 'stats'
  | 'logs'
  | 'integrations'
  | 'machines'
  | 'update'


export interface ViewEntry {
  readonly view: View
  readonly label: string
  readonly tagline: string
  readonly section: Section
}

const VIEW_HASHES: Record<View, string> = {
  overview: '#/overview',
  daemon: '#/daemon',
  providers: '#/providers',
  usage: '#/usage',
  stats: '#/stats',
  logs: '#/logs',
  integrations: '#/integrations',
  machines: '#/machines',
  update: '#/update',
}

const VIEW_BY_HASH: Record<string, View> = {
  '#/overview': 'overview',
  '#/daemon': 'daemon',
  '#/providers': 'providers',
  '#/usage': 'usage',
  '#/stats': 'stats',
  '#/logs': 'logs',
  '#/integrations': 'integrations',
  '#/machines': 'machines',
  '#/update': 'update',
}


export const VIEWS: readonly ViewEntry[] = [
  { view: 'overview', label: 'Overview', tagline: 'Fleet health at a glance', section: 'Overview' },
  { view: 'daemon', label: 'Daemon', tagline: 'Supervisor state and lifecycle', section: 'System' },
  { view: 'usage', label: 'Accounts', tagline: 'Per-account quota, pinning and login', section: 'Operate' },
  { view: 'stats', label: 'Stats', tagline: 'Request and token statistics', section: 'Operate' },
  { view: 'logs', label: 'Logs', tagline: 'Model requests and failover history', section: 'Operate' },
  { view: 'providers', label: 'Providers', tagline: 'Wires, models, credentials', section: 'Configure' },
  { view: 'integrations', label: 'Integrations', tagline: 'Codex / Grok / OMP apply and rollback', section: 'System' },
  { view: 'machines', label: 'Machines', tagline: 'Remote hosts over SSH', section: 'System' },
  { view: 'update', label: 'Update', tagline: 'App updates and channel status', section: 'System' },

]

export const SECTIONS: readonly Section[] = ['Overview', 'Operate', 'Configure', 'System']

export function hashFor(view: View): string {
  return VIEW_HASHES[view]
}

export function viewFromHash(raw: string): View {
  return VIEW_BY_HASH[raw] ?? 'overview'
}

export function readCurrentView(): View {
  return viewFromHash(window.location.hash)
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
