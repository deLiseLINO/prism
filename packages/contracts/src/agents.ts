export type AgentId = 'codex' | 'grok' | 'omp' | 'claude' | 'pi' | 'opencode' | 'opencode2' | 'hermes'

export type AgentInstallSource = 'npm' | 'pnpm' | 'bun' | 'brew' | 'script' | 'unknown'

export type AgentJobState =
  | 'idle'
  | 'running'
  | 'installing'
  | 'verifying'
  | 'succeeded'
  | 'failed'
  | 'unsupported'
  | 'interrupted'

export interface AgentJob {
  readonly agent: string
  readonly op: string
  readonly state: AgentJobState
  readonly method?: string
  readonly command?: string
  readonly output?: string
  readonly startedAt?: string
  readonly updatedAt?: string
  readonly error?: string
}

export interface AgentStatus {
  readonly id: AgentId
  readonly key: string
  readonly installed: boolean
  readonly source: AgentInstallSource | ''
  readonly path?: string
  readonly canUpdate: boolean
  readonly reason?: string
  readonly job: AgentJob
}

export interface AgentsView {
  readonly agents: readonly AgentStatus[]
  readonly actionsEnabled: boolean
}

export interface AgentJobRequest {
  readonly id: AgentId
  readonly force?: boolean
}

export type AgentJobReply =
  | { readonly ok: true; readonly job: AgentJob }
  | { readonly ok: false; readonly id: AgentId; readonly reason: string }
