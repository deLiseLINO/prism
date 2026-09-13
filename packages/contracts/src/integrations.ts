export type IntegrationId = 'codex' | 'grok' | 'omp' | 'claude' | 'pi' | 'opencode' | 'opencode2' | 'hermes'

export interface IntegrationRequest {
  readonly id: IntegrationId
  readonly force?: boolean
  readonly host?: HostId
}


export type HostId = string

export interface HostView {
  readonly id: HostId
  readonly local: boolean
  readonly status: string
  readonly detail?: string
  readonly address?: string
  readonly daemonPort?: number
}

export interface HostsView {
  readonly hosts: readonly HostView[]
}

export interface HostWrite {
  readonly id: string
  readonly address: string
  readonly expectedGeneration: number
  readonly daemonPort?: number
}

export interface HostMutationResponse {
  readonly generation: number
  readonly host: HostView
}

export type IntegrationApplyResult =
  | { readonly ok: true; readonly id: IntegrationId }
  | { readonly ok: false; readonly id: IntegrationId; readonly reason: string; readonly retryable?: boolean }

export interface IntegrationStatus {
  readonly id: IntegrationId
  readonly installed: boolean
  readonly managed: boolean
  readonly enabled: boolean
  readonly targetPath: string | null
  readonly endpoint: string | null
  readonly drift: boolean
  readonly detail: string
}

export interface IntegrationsView {
  readonly generation: number
  readonly integrations: readonly IntegrationStatus[]
}

export interface IntegrationToggleWrite {
  readonly enabled: boolean
  readonly expectedGeneration: number
}

export interface IntegrationToggleResponse {
  readonly generation: number
  readonly enabled: boolean
}
