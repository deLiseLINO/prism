import { contextBridge, ipcRenderer, type IpcRendererEvent } from 'electron'
import { IpcChannel, type DaemonStatus, type PrismBridge } from '@prism/contracts'

const bridge: PrismBridge = {
  daemon: {
    status: () => ipcRenderer.invoke(IpcChannel.daemonGetStatus),
    start: () => ipcRenderer.invoke(IpcChannel.daemonStart),
    stop: () => ipcRenderer.invoke(IpcChannel.daemonStop),
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
    status: () => ipcRenderer.invoke(IpcChannel.integrationStatus),
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
}

contextBridge.exposeInMainWorld('prism', bridge)
