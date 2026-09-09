import { app, nativeImage, type BrowserWindow } from 'electron'
import path from 'node:path'
import { DAEMON_HOST, IpcChannel } from '@prism/contracts'
import { loadDesktopConfig } from './config'
import { DaemonSupervisor } from './daemon/supervisor'
import { IntegrationApi } from './integrations'
import { registerIpc } from './ipc'
import { ManagementProxy } from './management'
import { TrayController } from './tray'
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
  const config = await loadDesktopConfig()
  if (config.userDataPath !== null) app.setPath('userData', config.userDataPath)
  if (!app.requestSingleInstanceLock()) {
    app.quit()
    return
  }
  const icon = nativeImage.createFromPath(path.join(__dirname, '..', '..', 'resources', 'icon.png'))
  if (process.platform === 'darwin') app.dock?.setIcon(icon)
  if (process.platform !== 'darwin') app.commandLine.appendSwitch('icon', path.join(__dirname, '..', '..', 'resources', 'icon.png'))
  let mainWindow: BrowserWindow | null = null
  let quitting = false
  const endpoint = `http://${DAEMON_HOST}:${config.port}`
  const supervisor = new DaemonSupervisor(endpoint, {
    port: config.port,
    daemonConfigPath: config.daemonConfigPath,
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
  const integrations = new IntegrationApi(management)
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
    void supervisor.stopForQuit().finally(() => app.quit())
  })

  void app.whenReady().then(() => {
    registerIpc({ supervisor, management, integrations })
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
  })
}

void bootstrap()
