export const IpcChannel = {
  daemonStatusEvent: 'prism:daemon-status-event',
  daemonGetStatus: 'prism:daemon-get-status',
  daemonStart: 'prism:daemon-start',
  daemonStop: 'prism:daemon-stop',
  managementRequest: 'prism:management-request',
  integrationApply: 'prism:integration-apply',
  integrationRollback: 'prism:integration-rollback',
  integrationStatus: 'prism:integration-status',
  shellOpenExternal: 'prism:shell-open-external',
  clipboardWrite: 'prism:clipboard-write',
  windowSetTheme: 'prism:window-set-theme',
  updaterStatusEvent: 'prism:updater-status-event',
  updaterGetStatus: 'prism:updater-get-status',
  updaterCheck: 'prism:updater-check',
  updaterInstall: 'prism:updater-install',
} as const

export type IpcChannel = (typeof IpcChannel)[keyof typeof IpcChannel]
