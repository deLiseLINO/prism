import type { DaemonStatus } from './daemon'
import type { HostsView, IntegrationApplyResult, IntegrationRequest, IntegrationStatus } from './integrations'
import type { ManagementCall, ManagementReply } from './management'

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
    readonly status: (host?: string) => Promise<readonly IntegrationStatus[]>
    readonly hosts: () => Promise<HostsView>
  }
  readonly shell: {
    readonly openExternal: (url: string) => Promise<void>
  },
  readonly window: {
    readonly setTheme: (theme: 'dark' | 'light') => Promise<void>
  }
}
