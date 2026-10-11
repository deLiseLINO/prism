import { BrowserWindow, clipboard, ipcMain, shell, type IpcMainInvokeEvent, type WebContents } from 'electron'
import { IpcChannel, type ReportContext } from '@prism/contracts'
import type { DaemonSupervisor } from './daemon/supervisor'
import { IntegrationApi, parseIntegrationRequest } from '../shared/integrations'
import { AgentsApi, parseAgentJobRequest } from '../shared/agents'
import { ManagementProxy, validateManagementCall } from '../shared/management'
import type { HostProxyRegistry } from './hosts/registry'
import { installHostDaemon, parseHostInstallRequest, type HostInstallDeps } from './hosts/install'
import { evaluateExternalNavigation, DEFAULT_NAVIGATION_POLICY } from './window/navigation'
import { titleBarOptions } from './window'
import { applyWindowMaterial, parseWindowMaterialRequest } from './window-material'
import type { UpdaterService } from './updater/updater'
import { buildSnapshot, reportSource, sendReport } from './report'

export interface IpcWiring {
  readonly supervisor: DaemonSupervisor
  readonly proxies: HostProxyRegistry
  readonly integrations: IntegrationApi
  readonly updater: UpdaterService
  readonly agents: AgentsApi
  readonly hostInstaller: HostInstallDeps
}

export type { HostInstallDeps } from './hosts/install'

function openExternal(url: unknown): Promise<void> {
  if (typeof url !== 'string' || url === '') {
    return Promise.reject(new Error('prism: openExternal requires a non-empty url string'))
  }
  let parsed: URL
  try {
    parsed = new URL(url)
  } catch {
    return Promise.reject(new Error(`prism: openExternal url is not parseable: ${url}`))
  }
  const verdict = evaluateExternalNavigation(parsed.toString(), parsed.origin, DEFAULT_NAVIGATION_POLICY)
  if (verdict.action === 'deny') {
    return Promise.reject(new Error(`prism: openExternal refused: ${verdict.reason}`))
  }
  return shell.openExternal(parsed.toString()).catch((error: unknown) => {
    throw error instanceof Error ? error : new Error(String(error))
  })
}

function trustedSender(event: IpcMainInvokeEvent): WebContents {
  const sender = event.sender
  if (sender === null || sender.isDestroyed()) {
    throw new Error('untrusted sender')
  }
  return sender
}

export function registerIpc(wiring: IpcWiring): void {
  ipcMain.handle(IpcChannel.daemonGetStatus, (event) => {
    trustedSender(event)
    return wiring.supervisor.status
  })
  ipcMain.handle(IpcChannel.managementRequest, async (event, input: unknown) => {
    trustedSender(event)
    const call = validateManagementCall(input)
    const target = await wiring.proxies.route(call.host)
    return target.call(call)
  })

  ipcMain.handle(IpcChannel.integrationApply, (event, input: unknown) => {
    trustedSender(event)
    return wiring.integrations.apply(parseIntegrationRequest(input))
  })
  ipcMain.handle(IpcChannel.integrationRollback, (event, input: unknown) => {
    trustedSender(event)
    return wiring.integrations.rollback(parseIntegrationRequest(input))
  })
  ipcMain.handle(IpcChannel.integrationStatus, (event, host: unknown) => {
    trustedSender(event)
    if (host !== undefined && typeof host !== 'string') {
      throw new Error('prism: integration host must be a string')
    }
    return wiring.integrations.status(host)
  })
  ipcMain.handle(IpcChannel.hostsList, (event) => {
    trustedSender(event)
    return wiring.integrations.hosts()
  })
  ipcMain.handle(IpcChannel.hostsInstall, async (event, input: unknown) => {
    trustedSender(event)
    return installHostDaemon(wiring.hostInstaller, parseHostInstallRequest(input))
  })

  ipcMain.handle(IpcChannel.shellOpenExternal, (event, input: unknown) => {
    trustedSender(event)
    return openExternal(input)
  })
  ipcMain.handle(IpcChannel.clipboardWrite, (event, input: unknown) => {
    trustedSender(event)
    if (typeof input !== 'string') {
      throw new Error('prism: clipboard write requires a string')
    }
    clipboard.writeText(input)
  })
  ipcMain.handle(IpcChannel.agentInstall, (event, input: unknown) => {
    trustedSender(event)
    return wiring.agents.install(parseAgentJobRequest(input))
  })
  ipcMain.handle(IpcChannel.agentUpdate, (event, input: unknown) => {
    trustedSender(event)
    return wiring.agents.update(parseAgentJobRequest(input))
  })
  ipcMain.handle(IpcChannel.agentJob, (event, input: unknown) => {
    trustedSender(event)
    return wiring.agents.job(parseAgentJobRequest(input))
  })
  ipcMain.handle(IpcChannel.agentStatus, (event) => {
    trustedSender(event)
    return wiring.agents.status()
  })
  ipcMain.handle(IpcChannel.windowSetTheme, (event, input: unknown) => {
    const sender = trustedSender(event)
    if (input !== 'dark' && input !== 'light') {
      throw new Error('prism: window theme must be dark or light')
    }
    const window = BrowserWindow.fromWebContents(sender)
    const overlay = titleBarOptions(input).titleBarOverlay
    if (overlay !== undefined && window !== null && !window.isDestroyed()) window.setTitleBarOverlay(overlay)
  })
  ipcMain.handle(IpcChannel.windowSetMaterial, (event, input: unknown) => {
    const sender = trustedSender(event)
    const request = parseWindowMaterialRequest(input)
    const window = BrowserWindow.fromWebContents(sender)
    if (process.platform !== 'darwin' || window === null || window.isDestroyed()) return false
    return applyWindowMaterial(window, request)
  })
  ipcMain.handle(IpcChannel.updaterGetStatus, (event) => {
    trustedSender(event)
    return wiring.updater.status
  })
  ipcMain.handle(IpcChannel.updaterCheck, (event) => {
    trustedSender(event)
    return wiring.updater.check()
  })
  ipcMain.handle(IpcChannel.updaterInstall, (event) => {
    trustedSender(event)
    wiring.updater.install()
  })
  ipcMain.handle(IpcChannel.updaterSetRcChannel, (event, input: unknown) => {
    trustedSender(event)
    if (typeof input !== 'boolean') throw new Error('prism: rc channel requires a boolean')
    wiring.updater.setRcChannel(input)
  })
  ipcMain.handle(IpcChannel.reportSnapshot, (event, input: unknown) => {
    trustedSender(event)
    const report = parseReport(input)
    const source = reportSource(wiring.supervisor.status, () => wiring.supervisor.logTail())
    return buildSnapshot(source, report.title, report.detail)
  })
  ipcMain.handle(IpcChannel.reportSend, async (event, input: unknown) => {
    trustedSender(event)
    const report = parseReport(input)
    const screenshot = await captureWindow(event.sender)
    const source = reportSource(wiring.supervisor.status, () => wiring.supervisor.logFile())
    return sendReport(source, report.target, report.title, report.detail, screenshot, report.context)
  })
}

export function parseReport(input: unknown): { title: string; detail: string; target: 'issue' | 'bot'; context: ReportContext } {
  if (typeof input !== 'object' || input === null) throw new Error('prism: report requires an object')
  const record = input as Record<string, unknown>
  if (typeof record.title !== 'string' || record.title === '') throw new Error('prism: report title requires a string')
  if (typeof record.detail !== 'string') throw new Error('prism: report detail requires a string')
  const target = record.target === undefined ? 'issue' : record.target
  if (target !== 'issue' && target !== 'bot') throw new Error('prism: report target must be issue or bot')
  return { title: record.title, detail: record.detail, target, context: parseContext(record.context) }
}

function parseContext(input: unknown): ReportContext {
  if (input === undefined) return { steps: [], failed: null }
  if (typeof input !== 'object' || input === null) throw new Error('prism: report context requires an object')
  const record = input as Record<string, unknown>
  if (!Array.isArray(record.steps) || record.steps.length > 8) throw new Error('prism: report steps must be an array of at most 8')
  return { steps: record.steps.map(parseStep), failed: parseFailed(record.failed) }
}

function parseStep(input: unknown): ReportContext['steps'][number] {
  if (typeof input !== 'object' || input === null) throw new Error('prism: report step requires an object')
  const record = input as Record<string, unknown>
  if (typeof record.at !== 'number' || !Number.isFinite(record.at)) throw new Error('prism: report step requires at')
  if (record.kind === 'navigate' && typeof record.view === 'string' && isFactValue(record.view)) {
    return { kind: 'navigate', view: record.view, at: record.at }
  }
  if (record.kind === 'action' && typeof record.name === 'string' && isFactValue(record.name)) {
    return { kind: 'action', name: record.name, at: record.at }
  }
  throw new Error('prism: report step is invalid')
}

function parseFailed(input: unknown): ReportContext['failed'] {
  if (input === null) return null
  if (typeof input !== 'object' || input === null) throw new Error('prism: report request requires an object')
  const record = input as Record<string, unknown>
  if (record.method !== 'GET' && record.method !== 'POST' && record.method !== 'PUT' && record.method !== 'DELETE') {
    throw new Error('prism: report request method is invalid')
  }
  if (typeof record.path !== 'string' || !isFactValue(record.path) || record.path.includes('?') || record.path.includes('#')) {
    throw new Error('prism: report request path is invalid')
  }
  if (typeof record.status !== 'number' || !Number.isInteger(record.status) || record.status < 0 || record.status > 599) {
    throw new Error('prism: report request status is invalid')
  }
  if (typeof record.code !== 'string' || !isFactValue(record.code)) throw new Error('prism: report request code is invalid')
  return { method: record.method, path: record.path, status: record.status, code: record.code }
}

function isFactValue(value: string): boolean {
  return value !== '' && value.length <= 120 && !value.includes(':') && !value.includes('\n') && !value.includes('\r')
}

async function captureWindow(sender: WebContents): Promise<string | null> {
  const window = BrowserWindow.fromWebContents(sender)
  if (window === null || window.isDestroyed()) return null
  const image = await window.webContents.capturePage()
  return image.isEmpty() ? null : `data:image/png;base64,${image.toPNG().toString('base64')}`
}
