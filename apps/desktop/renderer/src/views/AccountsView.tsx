import { useEffect, useMemo, useState } from 'react'
import type {
  AccountView,
  AccountsView,
  PoolSettingsView,
  ProviderView,
  ProvidersView,
  QuotaResponse,
} from '@prism/contracts'
import { AsyncBoundary, Banner, Button, Card, Confirm, Empty, Field, Row, SearchInput, Select, Stack, TextInput, Toggle } from '../components/Ui'
import { useAsync, useTask, describeError } from '../useAsync'
import { ApiError, api, type ProviderWrite } from '../api'

// Wires whose accounts expose a live per-account quota probe in the daemon.
const LIVE_QUOTA_WIRES: Record<string, true> = { codex: true, antigravity: true }

const DEFAULT_AUTO_SWITCH_THRESHOLD = 0.85

interface Grouped {
  readonly provider: string
  readonly accounts: readonly AccountView[]
  readonly pool: PoolSettingsView | null
  readonly poolProvider: ProviderView | null
  readonly poolGeneration: number
}

function groupByProvider(accounts: AccountsView, providers: ProvidersView): readonly Grouped[] {
  const byProvider = new Map<string, AccountView[]>()
  for (const account of accounts.accounts) {
    const existing = byProvider.get(account.provider) ?? []
    existing.push(account)
    byProvider.set(account.provider, existing)
  }
  const groups: Grouped[] = []
  for (const [provider, list] of byProvider.entries()) {
    const match = providers.providers.find((entry) => entry.id === provider)
    groups.push({
      provider,
      accounts: list.slice().sort((a, b) => b.priority - a.priority || a.id.localeCompare(b.id)),
      pool: match?.pool ?? null,
      poolProvider: match ?? null,
      poolGeneration: providers.generation,
    })
  }
  return groups.sort((a, b) => a.provider.localeCompare(b.provider))
}

const STRATEGY_OPTIONS = [
  { value: 'quota', label: 'quota' },
  { value: 'round_robin', label: 'round_robin' },
  { value: 'fill_first', label: 'fill_first' },
] as const

const AFFINITY_OPTIONS = [
  { value: 'sticky', label: 'sticky' },
  { value: 'off', label: 'off' },
] as const

interface AccountRowProps {
  readonly account: AccountView
  readonly wire: string | null
  readonly onRefresh: () => void
}

function AccountRow({ account, wire, onRefresh }: AccountRowProps): JSX.Element {
  const [priorityDraft, setPriorityDraft] = useState<string>(String(account.priority))
  const [confirming, setConfirming] = useState(false)
  const task = useTask()
  const paused = account.state === 'paused'
  const priorityValid = priorityDraft.trim() !== '' && Number.isInteger(Number(priorityDraft))

  useEffect(() => {
    setPriorityDraft(String(account.priority))
  }, [account.priority])

  async function pause(): Promise<void> {
    const result = await task.run(() => api.pauseAccount(account.id, account.version))
    if (result === undefined) return
    onRefresh()
  }
  async function resume(): Promise<void> {
    const result = await task.run(() => api.resumeAccount(account.id, account.version))
    if (result === undefined) return
    onRefresh()
  }
  async function setPriority(): Promise<void> {
    if (!priorityValid) return
    const next = Number(priorityDraft)
    const result = await task.run(() => api.setPriority(account.id, account.version, next))
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
      <td className="cell-mono">{account.id}</td>
      <td>
        <span className={`badge badge--${badgeTone(account.state)}`}>{account.state}</span>
      </td>
      <td>
        <div className="row row--start row--tight">
          <TextInput
            id={`prio-${account.id}`}
            type="number"
            value={priorityDraft}
            onChange={setPriorityDraft}
            ariaLabel={`Priority for ${account.id}`}
          />
          <Button tone="ghost" size="sm" onClick={() => void setPriority()} disabled={task.running || !priorityValid} busy={task.running}>
            Save
          </Button>
        </div>
        {!priorityValid ? (
          <div className="inline-error">
            <span>Priority must be an integer</span>
          </div>
        ) : null}
      </td>
      <td>
        {wire !== null && LIVE_QUOTA_WIRES[wire] === true ? (
          <LiveQuotaCell accountId={account.id} />
        ) : account.quota.limit == null ? (
          <span className="meta">no quota</span>
        ) : (
          <QuotaBar account={account} />
        )}
      </td>
      <td title={account.cooldownUntil}>{formatCooldownUntil(account.cooldownUntil)}</td>
      <td>
        <Row gap="tight" align="end">
          {paused ? (
            <Button tone="primary" size="sm" onClick={() => void resume()} disabled={task.running} busy={task.running}>
              Resume
            </Button>
          ) : (
            <Button tone="ghost" size="sm" onClick={() => void pause()} disabled={task.running} busy={task.running}>
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
            <Button tone="danger" size="sm" onClick={() => setConfirming(true)} disabled={task.running}>
              Remove
            </Button>
          )}
        </Row>
        {task.error !== null ? (
          <div className="inline-error">
            <span>{describeError(task.error)}</span>
            {task.error instanceof ApiError && task.error.code === 'stale_version' ? (
              <Button tone="ghost" size="sm" onClick={onRefresh}>Re-fetch account</Button>
            ) : null}
          </div>
        ) : null}
      </td>
    </tr>
  )
}

function badgeTone(state: AccountView['state']): 'ok' | 'warn' | 'error' | 'muted' {
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

function QuotaBar({ account }: { readonly account: AccountView }): JSX.Element {
  const limit = account.quota.limit ?? 0
  const pct = limit === 0 ? 0 : Math.max(0, Math.min(100, Math.round((account.quota.used / limit) * 100)))
  return (
    <div className={`quota ${pct >= 85 ? 'quota--hot' : ''}`}>
      <div className="quota__track">
        <div className="quota__fill" style={{ width: `${pct}%` }} />
      </div>
      <p className="meta">
        {account.quota.used} / {limit === 0 ? '?' : limit}
      </p>
    </div>
  )
}

// Pure helpers below drive the live quota cell and are the test seam for quota rendering.

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
  return { kind: 'ready', used, ...(limit === undefined ? {} : { limit }), windowEnd, source }
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
  if (hours < 24) return rest === 0 ? `in ${hours} h` : `in ${hours} h ${rest} min`
  const days = Math.round(hours / 24)
  return `in ${days} d`
}

function LiveQuotaCell({ accountId }: { readonly accountId: string }): JSX.Element {
  const quota = useAsync<QuotaResponse>(() => api.quota(accountId), [accountId])
  const cell = quota.state.kind === 'ready' ? quotaCell(quota.state.value) : null

  return (
    <div className="quota-live" role="group" aria-label={`Quota for ${accountId}`}>
      {quota.state.kind === 'loading' || quota.state.kind === 'idle' ? (
        <p className="quota-live__state" role="status">
          Loading quota…
        </p>
      ) : null}
      {quota.state.kind === 'error' ? (
        <div className="quota-live__state quota-live__state--error" role="alert">
          <span>{describeError(quota.state.error)}</span>
          <button
            type="button"
            className="btn btn--ghost"
            aria-label={`Refresh quota for ${accountId}`}
            onClick={() => quota.refresh()}
          >
            Retry
          </button>
        </div>
      ) : null}
      {cell !== null && cell.kind === 'unavailable' ? (
        <p className="quota-live__state" role="status">
          quota unavailable
        </p>
      ) : null}
      {cell !== null && cell.kind === 'ready' ? (
        <>
          <div className="quota">
            <div className="quota__track">
              <div
                className="quota__fill"
                style={{ width: `${quotaFillPercent(cell)}%` }}
              />
            </div>
            <p className="meta">
              {cell.used} / {cell.limit === undefined ? '?' : cell.limit}
            </p>
          </div>
          <p className="meta quota-live__window">
            window ends {formatWindowEnd(cell.windowEnd)} · source {cell.source}
          </p>
          <button
            type="button"
            className="btn btn--ghost"
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

function quotaFillPercent(cell: Extract<QuotaCellState, { kind: 'ready' }>): number {
  const limit = cell.limit
  if (limit === undefined || limit === 0) return 0
  return Math.max(0, Math.min(100, Math.round((cell.used / limit) * 100)))
}

interface PolicyCardProps {
  readonly group: Grouped
  readonly onSaved: () => void
}

interface GroupProps {
  readonly group: Grouped
  readonly onRefresh: () => void
}

function PolicyCard({ group, onSaved }: PolicyCardProps): JSX.Element {
  const initial = useMemo<PoolSettingsView>(
    () =>
      group.pool ?? {
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
    [group.pool],
  )
  const [strategy, setStrategy] = useState<PoolSettingsView['strategy']>(initial.strategy)
  const [autoSwitch, setAutoSwitch] = useState<boolean>(initial.autoSwitch ?? true)
  const [threshold, setThreshold] = useState<string>(String(initial.autoSwitchThreshold ?? DEFAULT_AUTO_SWITCH_THRESHOLD))
  const [affinity, setAffinity] = useState<'sticky' | 'off'>(initial.affinity ?? 'sticky')
  const [pinned, setPinned] = useState<string>(initial.pinnedAccount ?? '')
  const task = useTask()
  const [open, setOpen] = useState(false)
  const policyBodyId = `policy-${group.provider}`

  const thresholdNumber = threshold.trim() === '' ? Number.NaN : Number(threshold)
  const thresholdValid = Number.isFinite(thresholdNumber) && thresholdNumber >= 0 && thresholdNumber <= 1

  useEffect(() => {
    setStrategy(initial.strategy)
    setAutoSwitch(initial.autoSwitch ?? true)
    setThreshold(String(initial.autoSwitchThreshold ?? DEFAULT_AUTO_SWITCH_THRESHOLD))
    setAffinity(initial.affinity ?? 'sticky')
    setPinned(initial.pinnedAccount ?? '')
  }, [initial])

  async function save(): Promise<void> {
    if (group.poolProvider === null) return
    if (!thresholdValid) return
    const provider = group.poolProvider
    const next: ProviderWrite = {
      id: provider.id,
      wire: provider.wire,
      baseURL: provider.baseURL,
      defaultModel: provider.defaultModel,
      models: [...(provider.models ?? [])],
      disabledModels: [...(provider.disabledModels ?? [])],
      enabled: provider.enabled,
      pool: {
        ...initial,
        strategy,
        autoSwitch,
        autoSwitchThreshold: thresholdNumber,
        affinity,
        pinnedAccount: pinned,
      },
      expectedGeneration: group.poolGeneration,
    }
    const result = await task.run(() => api.replaceProvider(provider.id, next))
    if (result === undefined) return
    onSaved()
  }

  const canEdit = group.poolProvider !== null

  return (
    <div className="accordion">
      <button
        type="button"
        className="accordion__toggle"
        aria-expanded={open}
        aria-controls={policyBodyId}
        onClick={() => setOpen((value) => !value)}
      >
        <span className={`accordion__chevron ${open ? 'accordion__chevron--open' : ''}`.trim()} aria-hidden="true">▶</span>
        Selection policy
      </button>
      {open ? (
        <div className="accordion__body" id={policyBodyId}>
          <div className="field-grid">
            <Field label="Strategy" htmlFor={`strat-${group.provider}`}>
              <Select<'quota' | 'round_robin' | 'fill_first'>
                id={`strat-${group.provider}`}
                value={strategy}
                onChange={setStrategy}
                options={STRATEGY_OPTIONS}
                disabled={!canEdit || task.running}
              />
            </Field>
            <Field label="Affinity" htmlFor={`aff-${group.provider}`}>
              <Select<'sticky' | 'off'>
                id={`aff-${group.provider}`}
                value={affinity}
                onChange={setAffinity}
                options={AFFINITY_OPTIONS}
                disabled={!canEdit || task.running}
              />
            </Field>
            <Field label="Threshold (0–1)" htmlFor={`thr-${group.provider}`}>
              <TextInput
                id={`thr-${group.provider}`}
                type="number"
                value={threshold}
                onChange={setThreshold}
                min={0}
                max={1}
                step={0.05}
                disabled={!canEdit || task.running}
              />
            </Field>
            <Field label="Pinned account" htmlFor={`pin-${group.provider}`}>
              <TextInput
                id={`pin-${group.provider}`}
                value={pinned}
                onChange={setPinned}
                disabled={!canEdit || task.running}
              />
            </Field>
          </div>
          {!thresholdValid ? (
            <div className="inline-error">
              <span>Threshold must be between 0 and 1</span>
            </div>
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
              {task.error instanceof ApiError && task.error.code === 'stale_generation' ? (
                <>
                  : someone else updated the config.
                  <Button tone="ghost" size="sm" onClick={onSaved}>Re-fetch configuration</Button>
                </>
              ) : null}
            </Banner>
          ) : null}
        </div>
      ) : null}
    </div>
  )
}

function Group({ group, onRefresh }: GroupProps): JSX.Element {
  const active = group.accounts.filter((account) => account.state === 'active').length
  return (
    <Card
      title={`${group.provider}`}
      description={`${group.accounts.length} account(s), ${active} active`}
      action={
        <Button tone="ghost" size="sm" onClick={onRefresh}>
          Refresh
        </Button>
      }
    >
      <PolicyCard group={group} onSaved={onRefresh} />
      <div className="table-wrap">
        <table className="table">
          <thead>
            <tr>
              <th scope="col">Account</th>
              <th scope="col">State</th>
              <th scope="col">Priority</th>
              <th scope="col">Quota</th>
              <th scope="col">Cooldown</th>
              <th scope="col">Actions</th>
            </tr>
          </thead>
          <tbody>
            {group.accounts.map((account) => (
              <AccountRow
                key={account.id}
                account={account}
                wire={group.poolProvider?.wire ?? null}
                onRefresh={onRefresh}
              />
            ))}
          </tbody>
        </table>
      </div>
    </Card>
  )
}

export function AccountsView(): JSX.Element {
  const accounts = useAsync<AccountsView>(() => api.accounts(), [])
  const providers = useAsync<ProvidersView>(() => api.providers(), [])
  const [query, setQuery] = useState('')

  async function refresh(): Promise<void> {
    accounts.refresh()
    providers.refresh()
  }

  return (
    <Stack gap="normal">
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
              const needle = query.trim().toLowerCase()
              const grouped = groupByProvider(list, plist)
              if (grouped.length === 0) {
                return <Empty title="No accounts configured." />
              }
              const groups = grouped
                .map((group) => ({
                  ...group,
                  accounts: group.accounts.filter(
                    (account) =>
                      needle === '' ||
                      account.id.toLowerCase().includes(needle) ||
                      account.state.toLowerCase().includes(needle),
                  ),
                }))
                .filter((group) => group.accounts.length > 0)
              return (
                <>
                  <div className="toolbar">
                    <div className="toolbar__filters">
                      <SearchInput
                        id="account-search"
                        value={query}
                        onChange={setQuery}
                        placeholder="Filter by account or state"
                      />
                    </div>
                    <div className="toolbar__actions">
                      <p className="meta">
                        {groups.reduce((total, group) => total + group.accounts.length, 0)} shown
                      </p>
                    </div>
                  </div>
                  {groups.length === 0 ? (
                    <Empty title="No accounts match the current filter." />
                  ) : (
                    groups.map((group) => (
                      <Group key={group.provider} group={group} onRefresh={() => void refresh()} />
                    ))
                  )}
                </>
              )
            }}
          </AsyncBoundary>
        )}
      </AsyncBoundary>
    </Stack>
  )
}
