export const IpcChannel = {
  daemonStatusEvent: 'prism:daemon-status-event',
  daemonGetStatus: 'prism:daemon-get-status',
  managementRequest: 'prism:management-request',
  integrationApply: 'prism:integration-apply',
  integrationRollback: 'prism:integration-rollback',
  integrationStatus: 'prism:integration-status',
  hostsList: 'prism:hosts-list',
  hostsInstall: 'prism:hosts-install',
  agentInstall: 'prism:agent-install',
  agentUpdate: 'prism:agent-update',
  agentJob: 'prism:agent-job',
  agentStatus: 'prism:agent-status',


  shellOpenExternal: 'prism:shell-open-external',
  clipboardWrite: 'prism:clipboard-write',
  windowSetTheme: 'prism:window-set-theme',
  windowSetMaterial: 'prism:window-set-material',
  updaterStatusEvent: 'prism:updater-status-event',
  updaterGetStatus: 'prism:updater-get-status',
  updaterCheck: 'prism:updater-check',
  updaterInstall: 'prism:updater-install',
  updaterSetRcChannel: 'prism:updater-set-rc-channel',
  reportSnapshot: 'prism:report-snapshot',
  reportSend: 'prism:report-send',
} as const

export type IpcChannel = (typeof IpcChannel)[keyof typeof IpcChannel]

// Range of the native macOS window background blur radius, in points.
export const DESKTOP_WINDOW_BLUR_RADIUS_MAX = 64
export const DESKTOP_WINDOW_BLUR_RADIUS_MIN = 1

export interface DesktopWindowMaterial {
  readonly material: 'opaque' | 'translucent'
  readonly blurRadius: number
}
