import { BrowserWindow, ipcMain, shell, type IpcMainInvokeEvent, type WebContents } from 'electron'
import { IpcChannel } from '@prism/contracts'
import type { DaemonSupervisor } from './daemon/supervisor'
import { IntegrationApi, parseIntegrationRequest } from './integrations'
import { ManagementProxy, validateManagementCall } from './management'
import { evaluateExternalNavigation, DEFAULT_NAVIGATION_POLICY } from './window/navigation'
import { titleBarOptions } from './window'

export interface IpcWiring {
  readonly supervisor: DaemonSupervisor
  readonly management: ManagementProxy
  readonly integrations: IntegrationApi
}

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
  ipcMain.handle(IpcChannel.daemonStart, (event) => {
    trustedSender(event)
    return wiring.supervisor.start()
  })
  ipcMain.handle(IpcChannel.daemonStop, (event) => {
    trustedSender(event)
    return wiring.supervisor.stop()
  })
  ipcMain.handle(IpcChannel.managementRequest, (event, input: unknown) => {
    trustedSender(event)
    return wiring.management.call(validateManagementCall(input))
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
  ipcMain.handle(IpcChannel.shellOpenExternal, (event, input: unknown) => {
    trustedSender(event)
    return openExternal(input)
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
}
