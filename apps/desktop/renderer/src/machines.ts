import type {
  HostId,
  HostView,
  RequestView,
  RequestsView,
  StatsModelView,
  StatsOverviewView,
  StatsProviderView,
  StatsResponseView,
} from '@prism/contracts'

export interface Machine {
  readonly id: HostId
  readonly label: string
  readonly local: boolean
  readonly note: string
}

// A tagged union rather than `'all' | HostId`: the daemon accepts a remote host named `all`.
export type MachineScope = { readonly kind: 'all' } | { readonly kind: 'machine'; readonly id: HostId }

export const ALL_MACHINES: MachineScope = { kind: 'all' }

export type MachineFetch<T> =
  | { readonly machine: HostId; readonly ok: true; readonly value: T }
  | { readonly machine: HostId; readonly ok: false; readonly message: string }

export interface MachineFailure {
  readonly machine: HostId
  readonly message: string
}

export type MachineRequest = RequestView & { readonly machine: HostId }

export interface MergedRequests {
  readonly requests: readonly MachineRequest[]
  readonly dropped: number
  readonly failures: readonly MachineFailure[]
  readonly counts: ReadonlyMap<HostId, number>
}

export type MachineTokens = Readonly<Record<HostId, number>>
export type MergedProvider = StatsProviderView & { readonly byMachine: MachineTokens }
export type MergedModel = StatsModelView & { readonly byMachine: MachineTokens }

export interface MergedStats {
  readonly overview: StatsOverviewView
  readonly providers: readonly MergedProvider[]
  readonly models: readonly MergedModel[]
  readonly failures: readonly MachineFailure[]
  readonly counts: ReadonlyMap<HostId, number>
}

export interface StackSegment {
  readonly machine: Machine
  readonly tokens: number
  readonly ratio: number
  readonly shade: number
}

export const MACHINE_SCOPE_STORAGE_KEY = 'prism-machine-scope'

const MACHINE_PREFIX = 'machine:'

export function machinesFrom(hosts: readonly HostView[]): readonly Machine[] {
  const local = hosts.filter((host) => host.local)
  const remote = hosts.filter((host) => !host.local && host.status === 'ok')
  return [...local, ...remote].map((host) =>
    host.local
      ? { id: host.id, label: 'This machine', local: true, note: 'this machine' }
      : { id: host.id, label: host.id, local: false, note: host.address ?? '' },
  )
}

export function machineLabel(machines: readonly Machine[], id: HostId): string {
  return machines.find((machine) => machine.id === id)?.label ?? id
}

export function scopeTargets(scope: MachineScope, machines: readonly Machine[]): readonly HostId[] {
  return scope.kind === 'all' ? machines.map((machine) => machine.id) : [scope.id]
}

export function resolveScope(scope: MachineScope, machines: readonly Machine[]): MachineScope {
  if (scope.kind === 'all') return scope
  return machines.some((machine) => machine.id === scope.id) ? scope : ALL_MACHINES
}

export function initialScope({
  activeHost,
  stored,
  machines,
}: {
  readonly activeHost: HostId
  readonly stored: MachineScope | null
  readonly machines: readonly Machine[]
}): MachineScope {
  const active = machines.find((machine) => machine.id === activeHost)
  if (active !== undefined && !active.local) return { kind: 'machine', id: active.id }
  return resolveScope(stored ?? ALL_MACHINES, machines)
}

export function encodeScope(scope: MachineScope): string {
  return scope.kind === 'all' ? 'all' : `${MACHINE_PREFIX}${scope.id}`
}

export function decodeScope(raw: string | null): MachineScope | null {
  if (raw === 'all') return ALL_MACHINES
  if (raw === null || !raw.startsWith(MACHINE_PREFIX) || raw.length === MACHINE_PREFIX.length) return null
  return { kind: 'machine', id: raw.slice(MACHINE_PREFIX.length) }
}

export function readStoredScope(): MachineScope | null {
  try {
    return decodeScope(window.localStorage.getItem(MACHINE_SCOPE_STORAGE_KEY))
  } catch {
    return null
  }
}

export function storeScope(scope: MachineScope): void {
  try {
    window.localStorage.setItem(MACHINE_SCOPE_STORAGE_KEY, encodeScope(scope))
  } catch {
    return
  }
}

export function fetchMachines<T>(
  hosts: readonly HostId[],
  load: (host: HostId) => Promise<T>,
): Promise<readonly MachineFetch<T>[]> {
  return Promise.all(
    hosts.map(async (machine): Promise<MachineFetch<T>> => {
      try {
        return { machine, ok: true, value: await load(machine) }
      } catch (error: unknown) {
        return { machine, ok: false, message: error instanceof Error ? error.message : String(error) }
      }
    }),
  )
}

export function unavailableError(failures: readonly MachineFailure[], machines: readonly Machine[]): Error {
  return new Error(failures.map((failure) => `${machineLabel(machines, failure.machine)}: ${failure.message}`).join('; '))
}

// Count shown next to a menu option; the all-machines total is only known when every machine reported.
export function scopeCount(scope: MachineScope, counts: ReadonlyMap<HostId, number>, machines: readonly Machine[]): number | undefined {
  if (scope.kind === 'machine') return counts.get(scope.id)
  let total = 0
  for (const machine of machines) {
    const count = counts.get(machine.id)
    if (count === undefined) return undefined
    total += count
  }
  return total
}

function startedMs(request: RequestView): number {
  const ms = Date.parse(request.startedAt)
  return Number.isNaN(ms) ? 0 : ms
}

export function mergeRequests(results: readonly MachineFetch<RequestsView>[]): MergedRequests {
  const requests: MachineRequest[] = []
  const failures: MachineFailure[] = []
  const counts = new Map<HostId, number>()
  let dropped = 0
  for (const result of results) {
    if (!result.ok) {
      failures.push({ machine: result.machine, message: result.message })
      continue
    }
    dropped += result.value.dropped
    counts.set(result.machine, result.value.requests.length)
    for (const request of result.value.requests) requests.push({ ...request, machine: result.machine })
  }
  requests.sort((a, b) => startedMs(b) - startedMs(a))
  return { requests, dropped, failures, counts }
}

type Totals = { -readonly [K in keyof StatsOverviewView]: number }

const TOTAL_KEYS = [
  'requests',
  'completed',
  'failed',
  'input_tokens',
  'output_tokens',
  'cached_tokens',
  'reasoning_tokens',
  'total_tokens',
  'measured',
] as const satisfies readonly (keyof StatsOverviewView)[]

function zeroTotals(): Totals {
  return {
    requests: 0,
    completed: 0,
    failed: 0,
    input_tokens: 0,
    output_tokens: 0,
    cached_tokens: 0,
    reasoning_tokens: 0,
    total_tokens: 0,
    measured: 0,
  }
}

function addTotals(target: Totals, row: StatsOverviewView): void {
  for (const key of TOTAL_KEYS) target[key] += row[key]
}

interface Group<K> {
  readonly key: K
  readonly totals: Totals
  readonly byMachine: Record<HostId, number>
}

function addToGroup<K>(groups: Map<string, Group<K>>, id: string, key: K, machine: HostId, row: StatsOverviewView): void {
  let group = groups.get(id)
  if (group === undefined) {
    group = { key, totals: zeroTotals(), byMachine: {} }
    groups.set(id, group)
  }
  addTotals(group.totals, row)
  group.byMachine[machine] = (group.byMachine[machine] ?? 0) + row.total_tokens
}

export function mergeStats(results: readonly MachineFetch<StatsResponseView>[], machineOrder: readonly HostId[]): MergedStats {
  const rank = (machine: HostId): number => {
    const index = machineOrder.indexOf(machine)
    return index === -1 ? machineOrder.length : index
  }
  const ordered = [...results].sort((a, b) => rank(a.machine) - rank(b.machine))
  const overview = zeroTotals()
  const providers = new Map<string, Group<{ readonly provider: string }>>()
  const models = new Map<string, Group<{ readonly model: string; readonly provider: string }>>()
  const failures: MachineFailure[] = []
  const counts = new Map<HostId, number>()
  for (const result of ordered) {
    if (!result.ok) {
      failures.push({ machine: result.machine, message: result.message })
      continue
    }
    const { value, machine } = result
    addTotals(overview, value.overview)
    counts.set(machine, value.overview.requests)
    for (const row of value.providers) addToGroup(providers, row.provider, { provider: row.provider }, machine, row)
    for (const row of value.models) {
      addToGroup(models, JSON.stringify([row.model, row.provider]), { model: row.model, provider: row.provider }, machine, row)
    }
  }
  return {
    overview,
    providers: [...providers.values()]
      .map((group) => ({ ...group.key, ...group.totals, byMachine: group.byMachine }))
      .sort((a, b) => b.total_tokens - a.total_tokens),
    models: [...models.values()]
      .map((group) => ({ ...group.key, ...group.totals, byMachine: group.byMachine }))
      .sort((a, b) => b.total_tokens - a.total_tokens),
    failures,
    counts,
  }
}

// Opacity of a machine's shade of the foreground color: local (first) is strongest.
export function machineShade(index: number, count: number): number {
  if (count <= 1) return 55
  return Math.round(70 - (48 * index) / (count - 1))
}

export function stackSegments(byMachine: MachineTokens, machines: readonly Machine[], max: number): readonly StackSegment[] {
  if (max <= 0) return []
  return machines.flatMap((machine, index) => {
    const tokens = byMachine[machine.id] ?? 0
    if (tokens <= 0) return []
    return [{ machine, tokens, ratio: Math.min(1, tokens / max), shade: machineShade(index, machines.length) }]
  })
}
