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
} as const

export type IpcChannel = (typeof IpcChannel)[keyof typeof IpcChannel]
