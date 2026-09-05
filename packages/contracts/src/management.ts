export type ManagementMethod = 'GET' | 'POST' | 'PUT' | 'DELETE'

export interface ManagementCall {
  readonly method: ManagementMethod
  readonly path: string
  readonly body?: unknown
}

export interface ManagementReply {
  readonly ok: boolean
  readonly status: number
  readonly body?: unknown
  readonly error?: string
}

// Pool fields rendered from internal/config.PoolSettings. Optional fields use `?` to mirror the
// `omitempty` JSON tags on the daemon side. The daemon always serializes the cooldown/probe
// durations and the failover settings as plain nanosecond integers, so they are required here.
export interface PoolSettingsView {
  readonly strategy: 'quota' | 'round_robin' | 'fill_first'
  readonly autoSwitch?: boolean
  readonly autoSwitchThreshold: number
  readonly affinity?: 'sticky' | 'off'
  readonly pinnedAccount?: string
  readonly accountsPath: string
  readonly maxFailovers: number
  readonly cooldownDefault: number
  readonly cooldownMax: number
  readonly probeEvery: number
}

// Provider response shape (internal/management/schema.go Provider). The daemon never echoes
// apiKeyRef or credential bytes; the credential carries only its masked state.
export interface ProviderView {
  readonly id: string
  readonly wire: string
  readonly baseURL?: string
  readonly defaultModel?: string
  readonly models?: readonly string[]
  readonly disabledModels?: readonly string[]
  readonly enabled?: boolean
  readonly pool?: PoolSettingsView
  readonly credential: { readonly state: 'set' | 'unset' | 'unknown' }
}

export interface ProvidersView {
  readonly generation: number
  readonly providers: readonly ProviderView[]
}

// Response of POST /api/v1/providers and PUT /api/v1/providers/{id}: the new generation plus
// the merged provider, whose hidden pool durations survive the write.
export interface ProviderMutationResponse {
  readonly generation: number
  readonly provider: ProviderView
}

// Quota snapshot rendered by the daemon for a single account (and embedded in every account
// view). `limit` is omitted by the daemon when unknown; `windowEnd` is always serialized and
// is the Go zero time ("0001-01-01T00:00:00Z") when never observed.
export interface QuotaView {
  readonly used: number
  readonly limit?: number
  readonly windowEnd: string
  readonly source: 'header' | 'endpoint' | 'report' | 'probe' | 'unknown'
}

// Response of GET /api/v1/accounts/{id}/quota.
export interface QuotaResponse {
  readonly account: string
  readonly quota: QuotaView
}

export interface AccountView {
  readonly id: string
  readonly provider: string
  readonly state: 'active' | 'cooling_down' | 'needs_reauth' | 'soft_avoid' | 'paused' | 'unknown'
  readonly priority: number
  readonly version: number
  readonly credentialGeneration: number
  readonly quota: QuotaView
  readonly cooldownUntil?: string
  readonly softAvoidUntil?: string
  readonly inFlight: number
}

export interface AccountsView {
  readonly accounts: readonly AccountView[]
}

export interface ComboView {
  readonly id: string
  readonly targets: readonly { readonly provider: string; readonly model: string; readonly weight: number }[]
  readonly strategy: 'failover' | 'round_robin'
  readonly stickyLimit: number
  readonly alias?: string
  readonly nativeAlias?: string
  readonly displayName?: string
  readonly imageInput?: boolean
}

export interface CombosView {
  readonly generation: number
  readonly combos: readonly ComboView[]
}

export interface RoutesView {
  readonly generation: number
  readonly routes: Readonly<Record<string, string>>
}

export interface UsageAccountView {
  readonly account: string
  readonly provider: string
  readonly state: string
  readonly used: number
  readonly limit?: number | null
  readonly windowEnd: string
  readonly source: string
}

export interface UsageView {
  readonly accounts: readonly UsageAccountView[]
}

export interface AuthStartView {
  readonly session: string
  readonly url: string
}

export type AuthSessionState =
  | 'pending'
  | 'complete'
  | 'failed'
  | 'authorized'
  | 'unauthorized'
  | 'unknown'

export interface AuthStatusView {
  readonly provider: string
  readonly state: AuthSessionState
}

export interface ModelCapsView {
  readonly reasoning: boolean
  readonly customTools: boolean
  readonly localShell: boolean
  readonly toolSearch: boolean
  readonly compaction: boolean
  readonly countTokens: boolean
  readonly parallelTools: boolean
}

export interface ModelView {
  readonly id: string
  readonly alias?: string
  readonly caps: ModelCapsView
}

export interface RouteWrite {
  readonly value: string
  readonly expectedGeneration: number
}