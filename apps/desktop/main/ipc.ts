import { BrowserWindow, clipboard, ipcMain, shell, type IpcMainInvokeEvent, type WebContents } from 'electron'
import { IpcChannel } from '@prism/contracts'
import type { DaemonSupervisor } from './daemon/supervisor'
import { IntegrationApi, parseIntegrationRequest } from '../shared/integrations'
import { AgentsApi, parseAgentJobRequest } from '../shared/agents'
import { ManagementProxy, validateManagementCall } from '../shared/management'
import type { HostProxyRegistry } from './hosts/registry'
import { installHostDaemon, parseHostInstallRequest, type HostInstallDeps } from './hosts/install'
import { evaluateExternalNavigation, DEFAULT_NAVIGATION_POLICY } from './window/navigation'
import { titleBarOptions } from './window'
import type { UpdaterService } from './updater/updater'


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
}
