import type { DaemonStatus } from './daemon'
import type { IntegrationApplyResult, IntegrationRequest, IntegrationStatus } from './integrations'
import type { ManagementCall, ManagementReply } from './management'
import type { UpdaterStatus } from './updater'

export type Unsubscribe = () => void

export interface PrismBridge {
  readonly daemon: {
    readonly status: () => Promise<DaemonStatus>
    readonly start: () => Promise<DaemonStatus>
    readonly stop: () => Promise<DaemonStatus>
    readonly onStatus: (listener: (status: DaemonStatus) => void) => Unsubscribe
  }
  readonly management: {
    readonly call: (request: ManagementCall) => Promise<ManagementReply>
  }
  readonly integrations: {
    readonly apply: (request: IntegrationRequest) => Promise<IntegrationApplyResult>
    readonly rollback: (request: IntegrationRequest) => Promise<IntegrationApplyResult>
    readonly status: () => Promise<readonly IntegrationStatus[]>
  }
  readonly shell: {
    readonly openExternal: (url: string) => Promise<void>
    readonly writeClipboard: (text: string) => Promise<void>
  }
  readonly window: {
    readonly setTheme: (theme: 'dark' | 'light') => Promise<void>
  }
  readonly updater: {
    readonly status: () => Promise<UpdaterStatus>
    readonly check: () => Promise<void>
    readonly install: () => Promise<void>
    readonly onStatus: (listener: (status: UpdaterStatus) => void) => Unsubscribe
  }
}
