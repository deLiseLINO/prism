import { contextBridge, ipcRenderer, webFrame, type IpcRendererEvent } from 'electron'
import { IpcChannel, type DaemonStatus, type PrismBridge, type UpdaterStatus } from '@prism/contracts'

const bridge: PrismBridge = {
  daemon: {
    status: () => ipcRenderer.invoke(IpcChannel.daemonGetStatus),
    onStatus: (listener) => {
      const handler = (_event: IpcRendererEvent, status: DaemonStatus) => listener(status)
      ipcRenderer.on(IpcChannel.daemonStatusEvent, handler)
      return () => {
        ipcRenderer.removeListener(IpcChannel.daemonStatusEvent, handler)
      }
    },
  },
  management: {
    call: (request) => ipcRenderer.invoke(IpcChannel.managementRequest, request),
  },
  integrations: {
    apply: (request) => ipcRenderer.invoke(IpcChannel.integrationApply, request),
    rollback: (request) => ipcRenderer.invoke(IpcChannel.integrationRollback, request),
    status: (host) => ipcRenderer.invoke(IpcChannel.integrationStatus, host),
    hosts: () => ipcRenderer.invoke(IpcChannel.hostsList),
    installDaemon: (request) => ipcRenderer.invoke(IpcChannel.hostsInstall, request),
  },

  agents: {
    install: (request) => ipcRenderer.invoke(IpcChannel.agentInstall, request),
    update: (request) => ipcRenderer.invoke(IpcChannel.agentUpdate, request),
    job: (request) => ipcRenderer.invoke(IpcChannel.agentJob, request),
    status: () => ipcRenderer.invoke(IpcChannel.agentStatus),
  },
  shell: {
    openExternal: (url) => ipcRenderer.invoke(IpcChannel.shellOpenExternal, url),
    writeClipboard: (text) => ipcRenderer.invoke(IpcChannel.clipboardWrite, text),
  },
  window: {
    setTheme: (theme) => ipcRenderer.invoke(IpcChannel.windowSetTheme, theme),
  },
  zoom: {
    factor: () => webFrame.getZoomFactor(),
  },
  updater: {
    status: () => ipcRenderer.invoke(IpcChannel.updaterGetStatus),
    check: () => ipcRenderer.invoke(IpcChannel.updaterCheck),
    install: () => ipcRenderer.invoke(IpcChannel.updaterInstall),
    onStatus: (listener) => {
      const handler = (_event: IpcRendererEvent, status: UpdaterStatus) => listener(status)
      ipcRenderer.on(IpcChannel.updaterStatusEvent, handler)
      return () => {
        ipcRenderer.removeListener(IpcChannel.updaterStatusEvent, handler)
      }
    },
  },
}

contextBridge.exposeInMainWorld('prism', bridge)
