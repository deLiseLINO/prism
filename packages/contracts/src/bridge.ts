import type { DaemonStatus } from './daemon'
import type { AgentJob, AgentJobReply, AgentJobRequest, AgentsView } from './agents'
import type { HostsView, IntegrationApplyResult, IntegrationRequest, IntegrationStatus } from './integrations'

import type { ManagementCall, ManagementReply } from './management'
import type { HostInstallReply, HostInstallRequest } from './hostinstall'
import type { UpdaterStatus } from './updater'

export type Unsubscribe = () => void

export interface PrismBridge {
  readonly daemon: {
    readonly status: () => Promise<DaemonStatus>
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
    readonly installDaemon: (request: HostInstallRequest) => Promise<HostInstallReply>
  }

  readonly agents: {
    readonly install: (request: AgentJobRequest) => Promise<AgentJobReply>
    readonly update: (request: AgentJobRequest) => Promise<AgentJobReply>
    readonly job: (request: AgentJobRequest) => Promise<AgentJob>
    readonly status: () => Promise<AgentsView>
  }
  readonly shell: {
    readonly openExternal: (url: string) => Promise<void>
    readonly writeClipboard: (text: string) => Promise<void>
  }
  readonly window: {
    readonly setTheme: (theme: 'dark' | 'light') => Promise<void>
  }
  readonly zoom: {
    readonly factor: () => number
  }
  readonly updater: {
    readonly status: () => Promise<UpdaterStatus>
    readonly check: () => Promise<void>
    readonly install: () => Promise<void>
    readonly onStatus: (listener: (status: UpdaterStatus) => void) => Unsubscribe
  }
}
