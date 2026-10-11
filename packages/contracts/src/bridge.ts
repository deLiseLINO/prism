import type { DaemonStatus } from './daemon'
import type { AgentJob, AgentJobReply, AgentJobRequest, AgentsView } from './agents'
import type { HostsView, IntegrationApplyResult, IntegrationRequest, IntegrationStatus } from './integrations'

import type { ManagementCall, ManagementReply } from './management'
import type { HostInstallReply, HostInstallRequest } from './hostinstall'
import type { UpdaterStatus } from './updater'
import type { DesktopWindowMaterial } from './ipc'

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
    readonly setMaterial: (input: DesktopWindowMaterial) => Promise<boolean>
  }
  readonly zoom: {
    readonly factor: () => number
  }
  readonly updater: {
    readonly status: () => Promise<UpdaterStatus>
    readonly check: () => Promise<void>
    readonly install: () => Promise<void>
    readonly setRcChannel: (on: boolean) => Promise<void>
    readonly onStatus: (listener: (status: UpdaterStatus) => void) => Unsubscribe
  }
  readonly report: {
    readonly snapshot: (title: string, detail: string) => Promise<ReportSnapshot>
    readonly send: (target: ReportTarget, title: string, detail: string, screenshot: string | null, context?: ReportContext) => Promise<string | null>
  }
}

export type ReportTarget = 'issue' | 'bot'

export interface ReportSnapshot {
  readonly title: string
  readonly body: string
  readonly issueUrl: string
  readonly workerConfigured: boolean
  readonly pageUrl: string | null
}

export type DiagnosticStep =
  | { readonly kind: 'navigate'; readonly view: string; readonly at: number }
  | { readonly kind: 'action'; readonly name: string; readonly at: number }

export interface FailedRequest {
  readonly method: 'GET' | 'POST' | 'PUT' | 'DELETE'
  readonly path: string
  readonly status: number
  readonly code: string
}

export interface ReportContext {
  readonly steps: readonly DiagnosticStep[]
  readonly failed: FailedRequest | null
}
