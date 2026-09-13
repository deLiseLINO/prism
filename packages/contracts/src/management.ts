export type ManagementMethod = 'GET' | 'POST' | 'PUT' | 'DELETE'

export interface ManagementCall {
  readonly method: ManagementMethod
  readonly path: string
  readonly body?: unknown
  readonly host?: string
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
  readonly syncedModels?: readonly string[]
  // Antigravity-only extras: rawModels flattens the live family members behind each
  // logical id; modelEfforts maps logical family ids to their effort rungs. Both stay
  // absent for every other wire, so old daemons parse unchanged.
  readonly rawModels?: readonly string[]
  readonly modelEfforts?: Readonly<Record<string, readonly string[]>>
  readonly modelSettings?: Readonly<Record<string, ModelSettingsView>>
  readonly enabled?: boolean
  readonly pool?: PoolSettingsView
  readonly credential: { readonly state: 'set' | 'unset' | 'unknown' }
}

export interface ProvidersView {
  readonly generation: number
  readonly contextWindow: number
  readonly visionSidecar: VisionSidecarView
  readonly providers: readonly ProviderView[]
}

// Mirrors internal/management/schema.go VisionSidecarWrite (PUT) and the
// visionSidecar object embedded in GET /api/v1/providers. Target is
// `provider/model` naming an enabled model with imageInput, or empty.
export interface VisionSidecarView {
  readonly enabled?: boolean
  readonly target?: string
}

export interface VisionSidecarWrite {
  readonly enabled: boolean
  readonly target?: string
  readonly expectedGeneration: number
}

// Mirrors internal/management/schema.go ContextWindowWrite (PUT /api/v1/context-window).
// A value of 0 resets to the daemon default (256k).
export interface ContextWindowWrite {
  readonly contextWindow: number
  readonly expectedGeneration: number
}

export interface ModelSettingsView {
  readonly contextWindow?: number
  readonly imageInput?: boolean
  readonly reasoningEfforts?: readonly string[]
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
export interface QuotaWindowView {
  readonly label: string
  readonly used: number
  readonly limit?: number
  readonly windowEnd: string
}

export interface QuotaView {
  readonly used: number
  readonly limit?: number
  readonly windowEnd: string
  readonly source: 'header' | 'endpoint' | 'report' | 'probe' | 'unknown'
  readonly windows?: readonly QuotaWindowView[]
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
  readonly email?: string
  readonly quota: QuotaView
}

export interface UsageView {
  readonly accounts: readonly UsageAccountView[]
}

// Response of GET /api/v1/stats. Mirrors internal/management/schema.go. Unlike the other
// views here, the keys are snake_case because the daemon's JSON tags are snake_case and the
// embedded StatsOverview flattens its totals into each model/provider row.
export type StatsRange = '1h' | '24h' | '7d' | '30d' | 'all'

export interface StatsOverviewView {
  readonly requests: number
  readonly completed: number
  readonly failed: number
  readonly input_tokens: number
  readonly output_tokens: number
  readonly cached_tokens: number
  readonly reasoning_tokens: number
  readonly total_tokens: number
  readonly measured: number
}

export interface StatsModelView extends StatsOverviewView {
  readonly model: string
  readonly provider: string
}

export interface StatsProviderView extends StatsOverviewView {
  readonly provider: string
}

export interface StatsResponseView {
  readonly range: StatsRange
  readonly overview: StatsOverviewView
  readonly models: readonly StatsModelView[]
  readonly providers: readonly StatsProviderView[]
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

// Response of GET /api/v1/requests: the daemon's in-memory request journal, newest first.
// `error` on an attempt is the classified/shortened message only; upstream bodies never appear.
export interface UsageBreakdownView {
  readonly input: number
  readonly output: number
  readonly cached: number
  readonly reasoning: number
  readonly total: number
}

export interface AttemptView {
  readonly provider: string
  readonly account: string
  readonly model: string
  readonly startedAt: string
  readonly durationMs: number
  readonly outcome: string
  readonly error?: string
}

export interface RequestView {
  readonly seq: number
  readonly requestId?: string
  readonly client: string
  readonly session?: string
  readonly model: string
  readonly startedAt: string
  readonly durationMs: number
  readonly status: 'open' | 'completed' | 'incomplete' | 'failed'
  readonly reason?: string
  readonly usage: UsageBreakdownView
  readonly attempts: readonly AttemptView[]
}

export interface RequestsView {
  readonly requests: readonly RequestView[]
  readonly dropped: number
}
