import { useEffect, useMemo, useState, type CSSProperties } from 'react'
import type {
  AccountView,
  AccountsView,
  PoolSettingsView,
  ProviderView,
  ProvidersView,
  QuotaResponse,
} from '@prism/contracts'
import {
  AsyncBoundary,
  Banner,
  Button,
  Confirm,
  Empty,
  Field,
  Row,
  Select,
  TextInput,
  Toggle,
} from '../components/Ui'
import { useAsync, useTask, describeError } from '../useAsync'
import { ApiError, api, type ProviderWrite } from '../api'

const LIVE_QUOTA_WIRES: Record<string, true> = {
  codex: true,
  antigravity: true,
}

const DEFAULT_AUTO_SWITCH_THRESHOLD = 0.85

const FILTERS = [
  { value: 'all', label: 'All' },
  { value: 'active', label: 'Active' },
  { value: 'cooling_down', label: 'Cooling' },
  { value: 'needs_reauth', label: 'Reauth' },
  { value: 'paused', label: 'Paused' },
] as const

type AccountFilter = (typeof FILTERS)[number]['value']

const STRATEGY_OPTIONS = [
  { value: 'quota', label: 'quota' },
  { value: 'round_robin', label: 'round_robin' },
  { value: 'fill_first', label: 'fill_first' },
] as const

const AFFINITY_OPTIONS = [
  { value: 'sticky', label: 'sticky' },
  { value: 'off', label: 'off' },
] as const

interface WireIndex {
  readonly wires: Readonly<Record<string, string>>
  readonly pools: Readonly<Record<string, PoolSettingsView | null>>
  readonly providers: Readonly<Record<string, ProviderView | null>>
  readonly generation: number
}

function indexProviders(list: ProvidersView): WireIndex {
  const wires: Record<string, string> = {}
  const pools: Record<string, PoolSettingsView | null> = {}
  const providers: Record<string, ProviderView | null> = {}
  for (const provider of list.providers) {
    wires[provider.id] = provider.wire
    pools[provider.id] = provider.pool ?? null
    providers[provider.id] = provider
  }
  return { wires, pools, providers, generation: list.generation }
}

function sortAccounts(list: readonly AccountView[]): readonly AccountView[] {
  return list
    .slice()
    .sort((a, b) => b.priority - a.priority || a.id.localeCompare(b.id))
}

function stateLabel(state: AccountView['state']): string {
  if (state === 'cooling_down') return 'cooling down'
  if (state === 'needs_reauth') return 'needs reauth'
  if (state === 'soft_avoid') return 'soft avoid'
  return state
}

function badgeTone(
  state: AccountView['state'],
): 'ok' | 'warn' | 'error' | 'muted' {
  switch (state) {
    case 'active':
      return 'ok'
    case 'paused':
      return 'muted'
    case 'needs_reauth':
      return 'error'
    case 'cooling_down':
    case 'soft_avoid':
      return 'warn'
    default:
      return 'muted'
  }
}

interface AccountRowProps {
  readonly account: AccountView
  readonly wire: string | null
  readonly onRefresh: () => void
}

function AccountRow({
  account,
  wire,
  onRefresh,
}: AccountRowProps): JSX.Element {
  const [priorityDraft, setPriorityDraft] = useState<string>(
    String(account.priority),
  )
  const [confirming, setConfirming] = useState(false)
  const task = useTask()
  const paused = account.state === 'paused'
  const priorityValid =
    priorityDraft.trim() !== '' && Number.isInteger(Number(priorityDraft))

  useEffect(() => {
    setPriorityDraft(String(account.priority))
  }, [account.priority])

  async function pause(): Promise<void> {
    const result = await task.run(() =>
      api.pauseAccount(account.id, account.version),
    )
    if (result === undefined) return
    onRefresh()
  }
  async function resume(): Promise<void> {
    const result = await task.run(() =>
      api.resumeAccount(account.id, account.version),
    )
    if (result === undefined) return
    onRefresh()
  }
  async function setPriority(): Promise<void> {
    if (!priorityValid) return
    const next = Number(priorityDraft)
    const result = await task.run(() =>
      api.setPriority(account.id, account.version, next),
    )
    if (result === undefined) return
    onRefresh()
  }
  async function remove(): Promise<void> {
    const result = await task.run(async (): Promise<true> => {
      await api.deleteAccount(account.id)
      return true
    })
    if (result === undefined) return
    setConfirming(false)
    onRefresh()
  }
  return (
    <tr>
      <td className="td-strong">{account.id}</td>
      <td>
        <span className={`badge badge--${badgeTone(account.state)}`}>
          {stateLabel(account.state)}
        </span>
      </td>
      <td>
        <span className="row row--start row--tight">
          <span className="num">{account.priority}</span>
          <TextInput
            id={`prio-${account.id}`}
            type="number"
            value={priorityDraft}
            onChange={setPriorityDraft}
            ariaLabel={`Priority for ${account.id}`}
          />
          <Button
            tone="ghost"
            size="sm"
            onClick={() => void setPriority()}
            disabled={task.running || !priorityValid}
            busy={task.running}
          >
            Save
          </Button>
        </span>
        {!priorityValid ? (
          <span className="meta">Priority must be an integer</span>
        ) : null}
      </td>
      <td>
        {wire !== null && LIVE_QUOTA_WIRES[wire] === true ? (
          <LiveQuotaCell accountId={account.id} />
        ) : account.quota.limit == null ? (
          <div className="quota-cell">
            <span className="num" style={{ color: 'var(--fg-subtle)' }}>
              {account.quota.used}, no limit exposed
            </span>
          </div>
        ) : (
          <QuotaBar account={account} />
        )}
      </td>
      <td>
        <span className="num" title={account.cooldownUntil}>
          {formatCooldownUntil(account.cooldownUntil)}
        </span>
      </td>
      <td>
        <span className="num">
          v{account.version} · gen {account.credentialGeneration}
        </span>
      </td>
      <td>
        <span className="num">{account.inFlight}</span>
      </td>
      <td>
        <Row gap="tight" align="end">
          {paused ? (
            <Button
              tone="primary"
              size="sm"
              onClick={() => void resume()}
              disabled={task.running}
              busy={task.running}
            >
              Resume
            </Button>
          ) : (
            <Button
              tone="ghost"
              size="sm"
              onClick={() => void pause()}
              disabled={task.running}
              busy={task.running}
            >
              Pause
            </Button>
          )}
          {confirming ? (
            <Confirm
              title={`Remove ${account.id}?`}
              detail="The account leaves the pool immediately."
              confirmLabel="Remove"
              busy={task.running}
              onCancel={() => setConfirming(false)}
              onConfirm={() => void remove()}
            />
          ) : (
            <Button
              tone="danger"
              size="sm"
              onClick={() => setConfirming(true)}
              disabled={task.running}
            >
              Remove
            </Button>
          )}
        </Row>
        {task.error !== null ? (
          <div className="alert" role="alert">
            <span>{describeError(task.error)}</span>
            {task.error instanceof ApiError &&
            task.error.code === 'stale_version' ? (
              <Button tone="ghost" size="sm" onClick={onRefresh}>
                Re-fetch account
              </Button>
            ) : null}
          </div>
        ) : null}
      </td>
    </tr>
  )
}

function QuotaBar({ account }: { readonly account: AccountView }): JSX.Element {
  const limit = account.quota.limit ?? 0
  const ratio = limit === 0 ? 0 : account.quota.used / limit
  const pct = Math.max(0, Math.min(1, ratio))
  const fill = ratio > 0.9 ? 'f-danger' : ratio > 0.7 ? 'f-warn' : ''
  return (
    <div className="quota-cell">
      <span className="num">
        {account.quota.used}/{limit === 0 ? '?' : limit}
      </span>
      <span className="bar">
        <span
          className={`bar-fill ${fill}`.trim()}
          style={{ '--w': pct } as CSSProperties}
        />
      </span>
    </div>
  )
}

export type QuotaCellState =
  | { readonly kind: 'unavailable' }
  | {
      readonly kind: 'ready'
      readonly used: number
      readonly limit?: number
      readonly windowEnd: string
      readonly source: QuotaResponse['quota']['source']
    }

export function quotaCell(response: QuotaResponse): QuotaCellState {
  if (response.quota.source === 'unknown') {
    return { kind: 'unavailable' }
  }
  const { used, limit, windowEnd, source } = response.quota
  return {
    kind: 'ready',
    used,
    ...(limit === undefined ? {} : { limit }),
    windowEnd,
    source,
  }
}

const ZERO_WINDOW_END = '0001-01-01T00:00:00Z'

export function formatWindowEnd(iso: string): string {
  if (iso === ZERO_WINDOW_END) return '—'
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return '—'
  const year = String(date.getUTCFullYear()).padStart(2, '0')
  const month = String(date.getUTCMonth() + 1).padStart(2, '0')
  const day = String(date.getUTCDate()).padStart(2, '0')
  const hours = String(date.getUTCHours()).padStart(2, '0')
  const minutes = String(date.getUTCMinutes()).padStart(2, '0')
  return `${year}-${month}-${day} ${hours}:${minutes}`
}

function formatCooldownUntil(iso: string | undefined): string {
  if (iso === undefined || iso === '') return '—'
  const end = new Date(iso)
  if (Number.isNaN(end.getTime())) return '—'
  const remaining = end.getTime() - Date.now()
  if (remaining <= 0) return 'cooldown ended'
  const minutes = Math.round(remaining / 60000)
  if (minutes < 1) return 'in <1 min'
  if (minutes < 60) return `in ${minutes} min`
  const hours = Math.floor(minutes / 60)
  const rest = minutes % 60
  if (hours < 24)
    return rest === 0 ? `in ${hours} h` : `in ${hours} h ${rest} min`
  const days = Math.round(hours / 24)
  return `in ${days} d`
}

function LiveQuotaCell({
  accountId,
}: {
  readonly accountId: string
}): JSX.Element {
  const quota = useAsync<QuotaResponse>(() => api.quota(accountId), [accountId])
  const cell =
    quota.state.kind === 'ready' ? quotaCell(quota.state.value) : null

  return (
    <div
      className="quota-cell"
      role="group"
      aria-label={`Quota for ${accountId}`}
    >
      {quota.state.kind === 'loading' || quota.state.kind === 'idle' ? (
        <span className="skel skel-num" aria-hidden="true" />
      ) : null}
      {quota.state.kind === 'error' ? (
        <div className="alert" role="alert">
          <span>{describeError(quota.state.error)}</span>
          <button
            type="button"
            className="btn btn--ghost btn--sm"
            aria-label={`Refresh quota for ${accountId}`}
            onClick={() => quota.refresh()}
          >
            Retry
          </button>
        </div>
      ) : null}
      {cell !== null && cell.kind === 'unavailable' ? (
        <span className="num" style={{ color: 'var(--fg-subtle)' }}>
          quota unavailable
        </span>
      ) : null}
      {cell !== null && cell.kind === 'ready' ? (
        <>
          <span
            className="num"
            title={`window ends ${formatWindowEnd(cell.windowEnd)} · source ${cell.source}`}
          >
            {cell.used}/{cell.limit === undefined ? '?' : cell.limit}
          </span>
          <span className="bar">
            <span
              className={`bar-fill ${quotaFillRatio(cell) > 0.9 ? 'f-danger' : quotaFillRatio(cell) > 0.7 ? 'f-warn' : ''}`.trim()}
              style={{ '--w': quotaFillRatio(cell) } as CSSProperties}
            />
          </span>
          <button
            type="button"
            className="btn btn--ghost btn--sm"
            aria-label={`Refresh quota for ${accountId}`}
            onClick={() => quota.refresh()}
          >
            Refresh
          </button>
        </>
      ) : null}
    </div>
  )
}

function quotaFillRatio(
  cell: Extract<QuotaCellState, { kind: 'ready' }>,
): number {
  const limit = cell.limit
  if (limit === undefined || limit === 0) return 0
  return Math.max(0, Math.min(1, cell.used / limit))
}

interface PolicyEditorProps {
  readonly provider: string
  readonly pool: PoolSettingsView | null
  readonly poolProvider: ProviderView | null
  readonly generation: number
  readonly onSaved: () => void
}

function PolicyEditor({
  provider,
  pool,
  poolProvider,
  generation,
  onSaved,
}: PolicyEditorProps): JSX.Element {
  const initial = useMemo<PoolSettingsView>(
    () =>
      pool ?? {
        strategy: 'quota',
        autoSwitchThreshold: DEFAULT_AUTO_SWITCH_THRESHOLD,
        affinity: 'sticky',
        pinnedAccount: '',
        accountsPath: '',
        maxFailovers: 3,
        cooldownDefault: 300000000000,
        cooldownMax: 900000000000,
        probeEvery: 60000000000,
      },
    [pool],
  )
  const [strategy, setStrategy] = useState<PoolSettingsView['strategy']>(
    initial.strategy,
  )
  const [autoSwitch, setAutoSwitch] = useState<boolean>(
    initial.autoSwitch ?? true,
  )
  const [threshold, setThreshold] = useState<string>(
    String(initial.autoSwitchThreshold ?? DEFAULT_AUTO_SWITCH_THRESHOLD),
  )
  const [affinity, setAffinity] = useState<'sticky' | 'off'>(
    initial.affinity ?? 'sticky',
  )
  const [pinned, setPinned] = useState<string>(initial.pinnedAccount ?? '')
  const task = useTask()
  const [open, setOpen] = useState(false)
  const policyBodyId = `policy-${provider}`

  const thresholdNumber =
    threshold.trim() === '' ? Number.NaN : Number(threshold)
  const thresholdValid =
    Number.isFinite(thresholdNumber) &&
    thresholdNumber >= 0 &&
    thresholdNumber <= 1

  useEffect(() => {
    setStrategy(initial.strategy)
    setAutoSwitch(initial.autoSwitch ?? true)
    setThreshold(
      String(initial.autoSwitchThreshold ?? DEFAULT_AUTO_SWITCH_THRESHOLD),
    )
    setAffinity(initial.affinity ?? 'sticky')
    setPinned(initial.pinnedAccount ?? '')
  }, [initial])

  async function save(): Promise<void> {
    if (poolProvider === null) return
    if (!thresholdValid) return
    const next: ProviderWrite = {
      id: poolProvider.id,
      wire: poolProvider.wire,
      baseURL: poolProvider.baseURL,
      defaultModel: poolProvider.defaultModel,
      models: [...(poolProvider.models ?? [])],
      disabledModels: [...(poolProvider.disabledModels ?? [])],
      enabled: poolProvider.enabled,
      pool: {
        ...initial,
        strategy,
        autoSwitch,
        autoSwitchThreshold: thresholdNumber,
        affinity,
        pinnedAccount: pinned,
      },
      expectedGeneration: generation,
    }
    const result = await task.run(() =>
      api.replaceProvider(poolProvider.id, next),
    )
    if (result === undefined) return
    onSaved()
  }

  const canEdit = poolProvider !== null

  return (
    <div className="policy">
      <div className="policy-head">
        <button
          type="button"
          className={`btn btn--ghost btn--sm ${open ? 'policy-open' : ''}`.trim()}
          aria-expanded={open}
          aria-controls={policyBodyId}
          onClick={() => setOpen((value) => !value)}
        >
          {open ? 'Hide' : 'Show'} selection policy for {provider}
        </button>
      </div>
      {open ? (
        <div id={policyBodyId} className="policy-body">
          <div className="field-grid">
            <Field label="Strategy" htmlFor={`strat-${provider}`}>
              <Select<'quota' | 'round_robin' | 'fill_first'>
                id={`strat-${provider}`}
                value={strategy}
                onChange={setStrategy}
                options={STRATEGY_OPTIONS}
                disabled={!canEdit || task.running}
              />
            </Field>
            <Field label="Affinity" htmlFor={`aff-${provider}`}>
              <Select<'sticky' | 'off'>
                id={`aff-${provider}`}
                value={affinity}
                onChange={setAffinity}
                options={AFFINITY_OPTIONS}
                disabled={!canEdit || task.running}
              />
            </Field>
            <Field label="Threshold (0–1)" htmlFor={`thr-${provider}`}>
              <TextInput
                id={`thr-${provider}`}
                type="number"
                value={threshold}
                onChange={setThreshold}
                min={0}
                max={1}
                step={0.05}
                disabled={!canEdit || task.running}
              />
            </Field>
            <Field label="Pinned account" htmlFor={`pin-${provider}`}>
              <TextInput
                id={`pin-${provider}`}
                value={pinned}
                onChange={setPinned}
                disabled={!canEdit || task.running}
              />
            </Field>
          </div>
          {!thresholdValid ? (
            <span className="meta">Threshold must be between 0 and 1</span>
          ) : null}
          <Row gap="loose" align="start">
            <Toggle
              checked={autoSwitch}
              onChange={setAutoSwitch}
              label="Auto-switch on quota threshold"
              disabled={!canEdit || task.running}
            />
            <Button
              tone="primary"
              size="sm"
              onClick={() => void save()}
              disabled={!canEdit || task.running || !thresholdValid}
              busy={task.running}
            >
              Save policy
            </Button>
          </Row>
          {!canEdit ? (
            <Banner tone="warn" title="No provider entry">
              Add a provider with this id before changing pool rules.
            </Banner>
          ) : null}
          {task.error !== null ? (
            <Banner tone="error" title="Policy write failed">
              {describeError(task.error)}
              {task.error instanceof ApiError &&
              task.error.code === 'stale_generation' ? (
                <>
                  : someone else updated the config.
                  <Button tone="ghost" size="sm" onClick={onSaved}>
                    Re-fetch configuration
                  </Button>
                </>
              ) : null}
            </Banner>
          ) : null}
        </div>
      ) : null}
    </div>
  )
}

export function AccountsView(): JSX.Element {
  const accounts = useAsync<AccountsView>(() => api.accounts(), [])
  const providers = useAsync<ProvidersView>(() => api.providers(), [])
  const [filter, setFilter] = useState<AccountFilter>('all')

  async function refresh(): Promise<void> {
    accounts.refresh()
    providers.refresh()
  }

  return (
    <section className="screen" aria-labelledby="h-accounts">
      <div className="screen-head" style={{ '--i': 0 } as CSSProperties}>
        <div>
          <h1 id="h-accounts">
            <svg width="19" height="19" className="h-ic">
              <use href="#i-heart" />
            </svg>
            Accounts
          </h1>
          <p className="sub">
            selection pool ordered by priority, live quota per account
            {accounts.state.kind === 'ready' ? (
              <>
                {' '}
                ·{' '}
                <span className="num">
                  {accounts.state.value.accounts.length}
                </span>{' '}
                total
              </>
            ) : null}
          </p>
        </div>
        <div className="head-actions">
          <div
            className="seg"
            role="group"
            aria-label="Filter accounts by state"
          >
            {FILTERS.map((entry) => (
              <button
                key={entry.value}
                type="button"
                className="seg-btn"
                aria-pressed={filter === entry.value}
                onClick={() => setFilter(entry.value)}
              >
                {entry.label}
              </button>
            ))}
          </div>
          <Button tone="ghost" size="sm" onClick={() => void refresh()}>
            Refresh
          </Button>
        </div>
      </div>
      <AsyncBoundary<AccountsView>
        state={accounts.state}
        loadingLabel="Loading accounts…"
        empty={<Empty title="No accounts configured." />}
        onRetry={() => accounts.refresh()}
      >
        {(list) => (
          <AsyncBoundary<ProvidersView>
            state={providers.state}
            loadingLabel="Loading providers…"
            empty={<Empty title="No providers configured." />}
            onRetry={() => providers.refresh()}
          >
            {(plist) => {
              const index = indexProviders(plist)
              const visible = sortAccounts(
                filter === 'all'
                  ? list.accounts
                  : list.accounts.filter((account) => account.state === filter),
              )
              return (
                <>
                  <section
                    className="panel card"
                    style={{ '--i': 1 } as CSSProperties}
                  >
                    <div className="tbl-wrap">
                      <table className="tbl table">
                        <thead>
                          <tr>
                            <th scope="col">Account</th>
                            <th scope="col">State</th>
                            <th scope="col">Priority</th>
                            <th scope="col">Quota</th>
                            <th scope="col">Window ends</th>
                            <th scope="col">Version</th>
                            <th scope="col">In-flight</th>
                            <th scope="col">Actions</th>
                          </tr>
                        </thead>
                        <tbody>
                          {visible.length === 0 ? (
                            <tr>
                              <td colSpan={8}>
                                <Empty title="No accounts in this state">
                                  Switch the filter back to all to see the full
                                  pool.
                                </Empty>
                              </td>
                            </tr>
                          ) : (
                            visible.map((account) => (
                              <AccountRow
                                key={account.id}
                                account={account}
                                wire={index.wires[account.provider] ?? null}
                                onRefresh={() => void refresh()}
                              />
                            ))
                          )}
                        </tbody>
                      </table>
                    </div>
                  </section>
                  <PolicyList index={index} onSaved={() => void refresh()} />
                </>
              )
            }}
          </AsyncBoundary>
        )}
      </AsyncBoundary>
    </section>
  )
}

function PolicyList({
  index,
  onSaved,
}: {
  readonly index: WireIndex
  readonly onSaved: () => void
}): JSX.Element | null {
  const providers = Object.keys(index.pools).sort((left, right) =>
    left.localeCompare(right),
  )
  if (providers.length === 0) return null
  return (
    <section
      className="panel card panel-pad"
      style={{ '--i': 2 } as CSSProperties}
    >
      <div className="panel-title">Selection policy</div>
      <p className="panel-sub">
        strategy, affinity and auto-switch per provider
      </p>
      <div className="divide" style={{ marginTop: 10 }}>
        {providers.map((provider) => (
          <PolicyEditor
            key={provider}
            provider={provider}
            pool={index.pools[provider] ?? null}
            poolProvider={index.providers[provider] ?? null}
            generation={index.generation}
            onSaved={onSaved}
          />
        ))}
      </div>
    </section>
  )
}
