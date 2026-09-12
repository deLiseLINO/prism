import type { PrismBridge, ManagementCall, ManagementReply, DaemonStatus, HostsView, UpdaterStatus, Unsubscribe } from '@prism/contracts'
import { DAEMON_HEALTH_PATH } from '@prism/contracts'
import { AgentsApi } from '../../shared/agents'
import { IntegrationApi } from '../../shared/integrations'
import { ManagementProxy } from '../../shared/management'

const HEALTH_POLL_MS = 10_000

// Empty base URL turns ManagementProxy into a same-origin fetch client: the
// daemon serves the UI and the API from the same port.
const proxy = new ManagementProxy('')
const agentsApi = new AgentsApi(proxy)
const integrationsApi = new IntegrationApi(proxy)

function electronBridge(): PrismBridge | undefined {
  if (typeof window === 'undefined') return undefined
  return 'prism' in window ? window.prism : undefined
}

export const bridge: PrismBridge = {
  get daemon() { return (electronBridge() ?? webBridge).daemon },
  get management() { return (electronBridge() ?? webBridge).management },
  get integrations() { return (electronBridge() ?? webBridge).integrations },
  get agents() { return (electronBridge() ?? webBridge).agents },
  get shell() { return (electronBridge() ?? webBridge).shell },
  get window() { return (electronBridge() ?? webBridge).window },
  get zoom() { return (electronBridge() ?? webBridge).zoom },
  get updater() { return (electronBridge() ?? webBridge).updater },
}

async function healthAsDaemonStatus(): Promise<DaemonStatus> {
  const reply = await proxy.call({ method: 'GET', path: DAEMON_HEALTH_PATH })
  if (!reply.ok || reply.status !== 200) {
    throw new Error(`prism: health check failed with status ${reply.status}`)
  }
  return { state: 'ready', attempt: 0, pid: null, endpoint: window.location.origin, startedAt: null, lastExit: null, lastError: null }
}

const webDaemon: PrismBridge['daemon'] = {
  status: healthAsDaemonStatus,
  onStatus: (listener: (status: DaemonStatus) => void): Unsubscribe => {
    const timer = window.setInterval(() => {
      void healthAsDaemonStatus().then(listener, () => undefined)
    }, HEALTH_POLL_MS)
    return () => window.clearInterval(timer)
  },
}

const webManagement: PrismBridge['management'] = {
  call: (request: ManagementCall): Promise<ManagementReply> => proxy.call(request),
}

const webIntegrations: PrismBridge['integrations'] = {
  apply: (request) => integrationsApi.apply(request),
  rollback: (request) => integrationsApi.rollback(request),
  status: (host) => integrationsApi.status(host),
  hosts: async (): Promise<HostsView> => {
    const reply = await proxy.call({ method: 'GET', path: '/api/v1/hosts' })
    if (!reply.ok || reply.status !== 200) {
      throw new Error(`prism: hosts list failed with status ${reply.status}`)
    }
    return reply.body as HostsView
  },
  installDaemon: () => Promise.reject(new Error('prism: daemon install is only available in the desktop app')),
}

const webAgents: PrismBridge['agents'] = {
  install: (request) => agentsApi.install(request),
  update: (request) => agentsApi.update(request),
  job: (request) => agentsApi.job(request),
  status: () => agentsApi.status(),
}

const webShell: PrismBridge['shell'] = {
  openExternal: (url: string): Promise<void> => {
    window.open(url, '_blank', 'noopener,noreferrer')
    return Promise.resolve()
  },
  writeClipboard: (text: string): Promise<void> => navigator.clipboard.writeText(text),
}

const webWindow: PrismBridge['window'] = {
  setTheme: () => Promise.resolve(),
}

const webZoom: PrismBridge['zoom'] = {
  factor: () => 1,
}

const updaterDisabled: UpdaterStatus = {
  state: 'disabled',
  currentVersion: '',
  availableVersion: null,
  downloadedVersion: null,
  progress: null,
  error: null,
  errorStage: null,
  canRetry: false,
  lastCheckedAt: null,
}

const webUpdater: PrismBridge['updater'] = {
  status: () => Promise.resolve(updaterDisabled),
  check: () => Promise.reject(new Error('prism: app updates are not available in web mode; update from the desktop app')),
  install: () => Promise.reject(new Error('prism: app updates are not available in web mode; update from the desktop app')),
  onStatus: (): Unsubscribe => () => undefined,
}

const webBridge: PrismBridge = {
  daemon: webDaemon,
  management: webManagement,
  integrations: webIntegrations,
  agents: webAgents,
  shell: webShell,
  window: webWindow,
  zoom: webZoom,
  updater: webUpdater,
}
