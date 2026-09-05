export type IntegrationId = 'codex' | 'grok' | 'omp' | 'claude' | 'pi' | 'opencode' | 'opencode2' | 'hermes'

export interface IntegrationRequest {
  readonly id: IntegrationId
}

export type IntegrationApplyResult =
  | { readonly ok: true; readonly id: IntegrationId }
  | { readonly ok: false; readonly id: IntegrationId; readonly reason: string }

export interface IntegrationStatus {
  readonly id: IntegrationId
  readonly installed: boolean
  readonly managed: boolean
  readonly targetPath: string | null
  readonly endpoint: string | null
  readonly drift: boolean
  readonly detail: string
}
