import { app, BrowserWindow, nativeImage } from 'electron'
import { accessSync, appendFileSync, constants, readFileSync } from 'node:fs'
import path, { join } from 'node:path'
import { DAEMON_HOST, IpcChannel } from '@prism/contracts'
import type { HostView } from '@prism/contracts'
import { HostProxyRegistry } from './hosts/registry'
import { loadDesktopConfig } from './config'
import { DaemonSupervisor } from './daemon/supervisor'
import { registerIpc } from './ipc'
import { AgentsApi } from '../shared/agents'
import { IntegrationApi } from '../shared/integrations'
import { ManagementProxy } from '../shared/management'
import { TrayController } from './tray'
import { UpdaterService } from './updater/updater'
import { locateResource } from './daemon/resources'
import { maintainHostDaemons } from './hosts/install'
import { createMainWindow } from './window'
import { CONNECT_LINK_SCHEME, PendingConnectLink, findConnectLink, isConnectLink } from './connect-link'

const COMMAND_TIMEOUT_MS = 20_000
const HEALTH_POLL_MS = 5_000
const HEALTH_PROBE_TIMEOUT_MS = 1_000
const RESTART_BASE_MS = 500
const RESTART_MAX_MS = 10_000
const MAX_RESTARTS = 5
const STABILITY_WINDOW_MS = 60_000

const connectLinks = new PendingConnectLink()

async function bootstrap(): Promise<void> {
  const env = process.env
  const config = await loadDesktopConfig()
  if (config.userDataPath !== null) app.setPath('userData', config.userDataPath)
  if (!app.requestSingleInstanceLock()) {
    app.quit()
    return
  }
  if (process.defaultApp) {
    const entry = process.argv[1]
    if (entry !== undefined) app.setAsDefaultProtocolClient(CONNECT_LINK_SCHEME, process.execPath, [path.resolve(entry)])
  } else {
    app.setAsDefaultProtocolClient(CONNECT_LINK_SCHEME)
  }
  const iconPath = locateResource('icon.png')
  const icon = nativeImage.createFromPath(iconPath)
  if (process.platform === 'darwin') app.dock?.setIcon(icon)
  if (process.platform !== 'darwin') app.commandLine.appendSwitch('icon', iconPath)
  let mainWindow: BrowserWindow | null = null
  let quitting = false
  const daemonLogPath = join(app.getPath('userData'), 'prism.log')
  const endpoint = `http://${DAEMON_HOST}:${config.port}`
  const supervisor = new DaemonSupervisor(endpoint, {
    port: config.port,
    daemonConfigPath: config.daemonConfigPath,
    webuiDir: config.webuiDir,
    logPath: daemonLogPath,
    commandTimeoutMs: COMMAND_TIMEOUT_MS,
    healthPollMs: HEALTH_POLL_MS,
    healthProbeTimeoutMs: HEALTH_PROBE_TIMEOUT_MS,
    restartBaseMs: RESTART_BASE_MS,
    restartMaxMs: RESTART_MAX_MS,
    maxRestarts: MAX_RESTARTS,
    stabilityWindowMs: STABILITY_WINDOW_MS,
  })
  const management = new ManagementProxy(endpoint)
  const proxies = new HostProxyRegistry(management, { fetchHosts: () => fetchDaemonHosts(management) })

  const appImagePath = process.platform === 'linux' ? process.env.APPIMAGE : undefined
  const canUpdateAppImage = (): boolean => {
    if (!appImagePath || !path.isAbsolute(appImagePath)) return false
    try {
      accessSync(path.dirname(appImagePath), constants.W_OK | constants.X_OK)
      return true
    } catch {
      return false
    }
  }
  const updater = new UpdaterService(
    { currentVersion: appVersion() },
    {
      autoUpdater: app.isPackaged &&
        (process.platform !== 'linux' || canUpdateAppImage()) ? require('electron-updater').autoUpdater : null,
      quitApp: () => app.quit(),
      canInstall: process.platform === 'linux' ? canUpdateAppImage : undefined,
      log: (line) => {
        try {
          appendFileSync(daemonLogPath, `${new Date().toISOString()} updater: ${line}\n`)
        } catch (error) {
          console.error('prism: cannot write updater log', error)
        }
      },
    },
  )

  const integrations = new IntegrationApi(management)
  const agents = new AgentsApi(management)
  const showWindow = (): void => {
    if (config.headless) return
    mainWindow?.show()
    mainWindow?.focus()
  }
  const tray = new TrayController(() => {
    showWindow()
  })

  connectLinks.subscribe(() => {
    showWindow()
    if (mainWindow !== null && !mainWindow.isDestroyed()) {
      mainWindow.webContents.send(IpcChannel.connectLinkEvent)
    }
  })
  offerConnectLink(findConnectLink(process.argv))

  app.on('second-instance', (_event, argv) => {
    showWindow()
    offerConnectLink(findConnectLink(argv))
  })

  app.on('activate', () => {
    if (!config.headless) mainWindow?.show()
  })

  app.on('window-all-closed', () => {})

  app.on('before-quit', (event) => {
    if (quitting) return
    quitting = true
    proxies.dispose()
    supervisor.release()
    if (updater.pendingInstall) {
      event.preventDefault()
      updater.performInstall()
    }
  })


  void app.whenReady().then(() => {
    registerIpc({ supervisor, proxies, integrations, updater, agents, connectLinks })


    proxies.start()
    updater.subscribe((status) => {
      if (mainWindow !== null && !mainWindow.isDestroyed()) {
        mainWindow.webContents.send(IpcChannel.updaterStatusEvent, status)
      }
    })
    supervisor.subscribe((status) => {

      if (!config.headless) tray.update(status)
      if (mainWindow !== null && !mainWindow.isDestroyed()) {
        mainWindow.webContents.send(IpcChannel.daemonStatusEvent, status)
      }
    })
    if (!config.headless) tray.init()
    const window = createMainWindow()
    window.on('close', (event) => {
      if (!quitting) {
        event.preventDefault()
        window.hide()
      }
    })
    mainWindow = window
    void supervisor.start().then((status) => {
      if (status.state === 'ready') void maintainHostDaemons(management)
    })
    updater.start()
  })
}

function offerConnectLink(link: string | null): void {
  if (link !== null) connectLinks.offer(link)
}

// macOS can deliver a link before the app is ready, so this listener cannot wait for bootstrap.
app.on('open-url', (event, url) => {
  event.preventDefault()
  if (isConnectLink(url)) connectLinks.offer(url)
})

void bootstrap()

function appVersion(): string {
  if (app.isPackaged) return app.getVersion()
  try {
    const pkg = JSON.parse(readFileSync(join(app.getAppPath(), 'package.json'), 'utf8')) as { version?: string }
    return pkg.version ?? app.getVersion()
  } catch {
    return app.getVersion()
  }
}

async function fetchDaemonHosts(management: ManagementProxy): Promise<readonly HostView[]> {
  const reply = await management.call({ method: 'GET', path: '/api/v1/hosts' })
  if (!reply.ok || typeof reply.body !== 'object' || reply.body === null || !('hosts' in reply.body)) {
    return []
  }
  const hosts = (reply.body as { hosts: unknown }).hosts
  if (!Array.isArray(hosts)) return []
  return hosts.filter((host): host is HostView => {
    if (typeof host !== 'object' || host === null) return false
    const record: Record<string, unknown> = host
    return typeof record['id'] === 'string' && typeof record['local'] === 'boolean' && typeof record['status'] === 'string'
  })
}

