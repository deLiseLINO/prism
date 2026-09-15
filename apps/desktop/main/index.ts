import { app, nativeImage, type BrowserWindow } from 'electron'
import { readFileSync } from 'node:fs'
import path, { join } from 'node:path'
import { DAEMON_HOST, IpcChannel } from '@prism/contracts'
import type { HostView } from '@prism/contracts'
import { HostProxyRegistry } from './hosts/registry'
import { loadDesktopConfig, PRISM_UPDATER_ENV } from './config'
import { DaemonSupervisor } from './daemon/supervisor'
import { registerIpc } from './ipc'
import { AgentsApi } from '../shared/agents'
import { IntegrationApi } from '../shared/integrations'
import { ManagementProxy } from '../shared/management'
import { TrayController } from './tray'
import { UpdaterService } from './updater/updater'
import { locateResource } from './daemon/resources'
import { createMainWindow } from './window'

const HEALTH_TIMEOUT_MS = 15_000
const HEALTH_INTERVAL_MS = 250
const HEALTH_PROBE_TIMEOUT_MS = 1_000
const STOP_GRACE_MS = 5_000
const RESTART_BASE_MS = 500
const RESTART_MAX_MS = 10_000
const MAX_RESTARTS = 5
const STABILITY_WINDOW_MS = 60_000

async function bootstrap(): Promise<void> {
  const env = process.env
  const config = await loadDesktopConfig()
  if (config.userDataPath !== null) app.setPath('userData', config.userDataPath)
  if (!app.requestSingleInstanceLock()) {
    app.quit()
    return
  }
  const iconPath = locateResource('icon.png')
  const icon = nativeImage.createFromPath(iconPath)
  if (process.platform === 'darwin') app.dock?.setIcon(icon)
  if (process.platform !== 'darwin') app.commandLine.appendSwitch('icon', iconPath)
  let mainWindow: BrowserWindow | null = null
  let quitting = false
  const endpoint = `http://${DAEMON_HOST}:${config.port}`
  const supervisor = new DaemonSupervisor(endpoint, {
    port: config.port,
    daemonConfigPath: config.daemonConfigPath,
    webuiDir: config.webuiDir,
    healthTimeoutMs: HEALTH_TIMEOUT_MS,
    healthIntervalMs: HEALTH_INTERVAL_MS,
    healthProbeTimeoutMs: HEALTH_PROBE_TIMEOUT_MS,
    stopGraceMs: STOP_GRACE_MS,
    restartBaseMs: RESTART_BASE_MS,
    restartMaxMs: RESTART_MAX_MS,
    maxRestarts: MAX_RESTARTS,
    stabilityWindowMs: STABILITY_WINDOW_MS,
  })
  const management = new ManagementProxy(endpoint)
  const proxies = new HostProxyRegistry(management, { fetchHosts: () => fetchDaemonHosts(management) })

  const updater = new UpdaterService(
    { currentVersion: appVersion(), updateUrl: config.updateUrl },
    {
      // The generic releases.prism.sh feed does not exist yet; shipping it
      // in installers puts every packaged app into a permanent update-error
      // state (checks 15s after launch, then every 240s, against a dead URL).
      // The updater stays off by default until a real feed is configured
      // (either a live generic server or GitHub Releases provider wired into
      // the release pipeline). PRISM_UPDATER=1 opts in explicitly; the env
      // escape hatch keeps local experiments against a private feed alive.
      autoUpdater:
        app.isPackaged && !config.updaterDisabled && env[PRISM_UPDATER_ENV] === '1'
          ? require('electron-updater').autoUpdater
          : null,
      quitApp: () => app.quit(),
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

  app.on('second-instance', () => {
    showWindow()
  })

  app.on('activate', () => {
    if (!config.headless) mainWindow?.show()
  })

  app.on('window-all-closed', () => {})

  app.on('before-quit', (event) => {
    if (quitting) return
    event.preventDefault()
    quitting = true
    proxies.dispose()
    void supervisor.stopForQuit().finally(() => {
      if (updater.pendingInstall) updater.performInstall()
      else app.quit()
    })
  })


  void app.whenReady().then(() => {
    registerIpc({ supervisor, proxies, integrations, updater, agents, hostInstaller: { management, onRegistered: () => { void proxies.sync() } } })


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
    void supervisor.start()
    updater.start()
  })
}

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

