export type Section = 'Overview' | 'Operate' | 'Configure' | 'System'

export type View =
  | 'overview'
  | 'daemon'
  | 'auth'
  | 'providers'
  | 'usage'
  | 'integrations'

export interface ViewEntry {
  readonly view: View
  readonly label: string
  readonly tagline: string
  readonly section: Section
}

const VIEW_HASHES: Record<View, string> = {
  overview: '#/overview',
  daemon: '#/daemon',
  auth: '#/auth',
  providers: '#/providers',
  usage: '#/usage',
  integrations: '#/integrations',
}

const VIEW_BY_HASH: Record<string, View> = {
  '#/overview': 'overview',
  '#/daemon': 'daemon',
  '#/auth': 'auth',
  '#/providers': 'providers',
  '#/usage': 'usage',
  '#/integrations': 'integrations',
}


export const VIEWS: readonly ViewEntry[] = [
  { view: 'overview', label: 'Overview', tagline: 'Fleet health at a glance', section: 'Overview' },
  { view: 'daemon', label: 'Daemon', tagline: 'Supervisor state and lifecycle', section: 'System' },
  { view: 'auth', label: 'Auth', tagline: 'OAuth flows per provider', section: 'Operate' },
  { view: 'usage', label: 'Accounts', tagline: 'Per-account quota, pinning and login', section: 'Operate' },
  { view: 'providers', label: 'Providers', tagline: 'Wires, models, credentials', section: 'Configure' },
  { view: 'integrations', label: 'Integrations', tagline: 'Codex / Grok / OMP apply and rollback', section: 'System' },
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
