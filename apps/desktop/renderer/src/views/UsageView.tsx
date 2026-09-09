import { useCallback, useEffect, useMemo, useRef, useState, type CSSProperties } from 'react'
import type {
  PoolSettingsView,
  ProviderView,
  ProvidersView,
  QuotaView,
  UsageAccountView,
  UsageView,
} from '@prism/contracts'
import { AsyncBoundary, Button, Confirm, Empty, SearchInput } from '../components/Ui'
import { useAsync, useTask, describeError } from '../useAsync'
import { api, type ProviderWrite } from '../api'
import { AddAccountModal } from '../components/AddAccountModal'

const AUTO_REFRESH_BACKGROUND_MS = 300_000
const AUTO_REFRESH_TICK_MS = 1_000
const MAX_CONCURRENT_QUOTA_LOADS = 3
const MAX_QUOTA_LOAD_ATTEMPTS = 3

type QuotaLoadMode = 'initial' | 'background' | 'manual'

const KNOWN_QUOTA_PROVIDERS: Record<string, true> = {
  antigravity: true,
  codex: true,
}

function badgeTone(state: string): 'ok' | 'warn' | 'error' | 'muted' {
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

function dotTone(state: string): 'ok' | 'warn' | 'danger' | 'muted' {
  if (state === 'active') return 'ok'
  if (state === 'needs_reauth') return 'danger'
  if (state === 'cooling_down' || state === 'soft_avoid') return 'warn'
  return 'muted'
}

function stateLabel(state: string): string {
  if (state === 'cooling_down') return 'cooling down'
  if (state === 'needs_reauth') return 'needs reauth'
  if (state === 'soft_avoid') return 'soft avoid'
  return state
}

interface UsageWindow {
  readonly label: string
  readonly used: number
  readonly limit?: number
  readonly windowEnd: string
}

function windowFamily(label: string): 'gemini' | 'claude' | '' {
  const lower = label.toLowerCase()
  if (lower.includes('gemini')) return 'gemini'
  if (lower.includes('claude')) return 'claude'
  return ''
}

function windowOrder(window: UsageWindow): number {
  const lower = window.label.toLowerCase()
  const type = lower.includes('5 hour') ? 0 : lower.includes('weekly') ? 1 : 2
  return windowFamily(window.label) === 'claude' ? 10 + type : type
}

export function quotaWindows(quota: QuotaView): readonly UsageWindow[] {
  if (quota.source === 'unknown') return []
  if (quota.windows !== undefined && quota.windows.length > 0) {
    return quota.windows
      .slice()
      .sort((left, right) => windowOrder(left) - windowOrder(right))
  }
  return []
}

export function windowHeader(label: string): string {
  const lower = label.toLowerCase()
  const family = windowFamily(label)
  if (family === '') {
    if (lower.includes('5 hour')) return '5 hour'
    if (lower.includes('weekly')) return 'Weekly'
    return label
  }
  if (lower.includes('5 hour')) return `5h ${family}`
  if (lower.includes('weekly')) return `weekly ${family}`
  return label
}

function formatReset(iso: string): string {
  const reset = new Date(iso)
  if (iso === '' || Number.isNaN(reset.getTime())) return 'Resets unknown'
  const remaining = reset.getTime() - Date.now()
  if (remaining <= 0) return 'Resets now'
  const now = new Date()
  const absolute =
    reset.getDate() === now.getDate() &&
    reset.getMonth() === now.getMonth() &&
    reset.getFullYear() === now.getFullYear()
      ? reset.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
      : reset.toLocaleDateString([], { weekday: 'short' }) +
        ' ' +
        reset.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  return `Resets ${absolute} (${formatRemaining(remaining)})`
}

function formatRemaining(remaining: number): string {
  const minutes = Math.round(remaining / 60000)
  if (minutes < 1) return '<1m'
  if (minutes < 60) return `${minutes}m`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) {
    const rest = minutes % 60
    return rest === 0 ? `${hours}h` : `${hours}h ${rest}m`
  }
  const days = Math.floor(hours / 24)
  const restHours = hours % 24
  return restHours === 0 ? `${days}d` : `${days}d ${restHours}h`
}

function accountLabel(row: UsageAccountView): string {
  return row.email === undefined || row.email.trim() === ''
    ? row.account
    : row.email
}

function leftRatio(window: UsageWindow): number {
  if (window.limit === undefined || window.limit === 0) return 0
  return Math.max(0, Math.min(1, 1 - window.used / window.limit))
}

function leftPercent(window: UsageWindow): number {
  return Math.round(leftRatio(window) * 100)
}

const SHORT_WINDOW_GRADIENT = 'linear-gradient(90deg, #4285F4, #34A853)'
const DEFAULT_WINDOW_GRADIENT = 'linear-gradient(90deg, #6C63FF, #D46DFF)'

export function windowGradient(window: UsageWindow): string {
  const family = windowFamily(window.label)
  if (family === 'gemini') return SHORT_WINDOW_GRADIENT
  if (family === 'claude') return DEFAULT_WINDOW_GRADIENT
  return window.label.toLowerCase().includes('5 hour')
    ? SHORT_WINDOW_GRADIENT
    : DEFAULT_WINDOW_GRADIENT
}

function hasLiveQuota(row: UsageAccountView): boolean {
  return KNOWN_QUOTA_PROVIDERS[row.provider] === true
}

export function providerWriteFrom(
  provider: ProviderView,
  generation: number,
  pool: PoolSettingsView,
): ProviderWrite {
  return {
    id: provider.id,
    wire: provider.wire,
    ...(provider.baseURL === undefined ? {} : { baseURL: provider.baseURL }),
    ...(provider.defaultModel === undefined ? {} : { defaultModel: provider.defaultModel }),
    models: provider.models ?? [],
    disabledModels: provider.disabledModels ?? [],
    ...(provider.syncedModels === undefined ? {} : { syncedModels: provider.syncedModels }),
    ...(provider.modelSettings === undefined ? {} : { modelSettings: provider.modelSettings }),
    ...(provider.enabled === undefined ? {} : { enabled: provider.enabled }),
    pool,
    expectedGeneration: generation,
  }
}

function hasLoadedQuota(row: UsageAccountView): boolean {
  return row.quota.source !== 'unknown' && quotaWindows(row.quota).length > 0
}

export function UsagePanel(): JSX.Element {
  const usage = useAsync<UsageView>(() => api.usage(), [])
  const [query, setQuery] = useState('')
  const [quotaLoading, setQuotaLoading] = useState<Record<string, boolean>>({})
  const [quotaFailed, setQuotaFailed] = useState<Record<string, string>>({})
  const [confirmingDelete, setConfirmingDelete] = useState<string | null>(null)
  const providers = useAsync<ProvidersView>(() => api.providers(), [])
  const [adding, setAdding] = useState(false)
  const pinTask = useTask()
  const deleteTask = useTask()
  const refreshAllTask = useTask()
  const loadingRef = useRef<Record<string, boolean>>({})
  const lastLoadedRef = useRef<Record<string, number>>({})
  const attemptsRef = useRef<Record<string, number>>({})

  const providersReady = providers.state.kind === 'ready' ? providers.state.value : null
  const providerById = useMemo(() => {
    const index: Record<string, ProviderView> = {}
    for (const provider of providersReady?.providers ?? []) {
      index[provider.id] = provider
    }
    return index
  }, [providersReady])
  const pinIndex = useMemo(() => {
    const pins: Record<string, string> = {}
    for (const provider of providersReady?.providers ?? []) {
      const pinned = provider.pool?.pinnedAccount
      if (pinned !== undefined && pinned !== '') pins[provider.id] = pinned
    }
    return pins
  }, [providersReady])
  const authProviders = useMemo(
    () =>
      (providersReady?.providers ?? []).filter(
        (provider) => provider.wire === 'codex' || provider.wire === 'antigravity',
      ),
    [providersReady],
  )
  const accounts = usage.state.kind === 'ready' ? usage.state.value.accounts : []

  const refreshQuota = useCallback(
    async (row: UsageAccountView, mode: QuotaLoadMode): Promise<void> => {
      if (loadingRef.current[row.account] === true) return
      loadingRef.current[row.account] = true
      if (mode !== 'background') {
        setQuotaLoading((prev) => ({ ...prev, [row.account]: true }))
      }
      setQuotaFailed((prev) => {
        if (prev[row.account] === undefined) return prev
        const next = { ...prev }
        delete next[row.account]
        return next
      })
      let settled = false
      try {
        const response = await (mode === 'manual'
          ? api.refreshQuota(row.account)
          : api.quota(row.account))
        attemptsRef.current[row.account] = 0
        lastLoadedRef.current[row.account] = Date.now()
        settled = true
        usage.refresh()
      } catch (err: unknown) {
        attemptsRef.current[row.account] = mode === 'manual'
          ? MAX_QUOTA_LOAD_ATTEMPTS
          : (attemptsRef.current[row.account] ?? 0) + 1
        const attempts = attemptsRef.current[row.account] ?? 0
        if (mode === 'manual' || attempts >= MAX_QUOTA_LOAD_ATTEMPTS) {
          const error = err instanceof Error ? err : new Error(String(err))
          setQuotaFailed((prev) => ({ ...prev, [row.account]: describeError(error) }))
        }
      } finally {
        delete loadingRef.current[row.account]
        if (settled || (attemptsRef.current[row.account] ?? 0) >= MAX_QUOTA_LOAD_ATTEMPTS) {
          setQuotaLoading((prev) => {
            if (prev[row.account] !== true) return prev
            const next = { ...prev }
            delete next[row.account]
            return next
          })
        }
      }
    },
    [usage],
  )

  const pumpQuotaLoads = useCallback(() => {
    const pending = accounts.filter(
      (row) =>
        hasLiveQuota(row) &&
        loadingRef.current[row.account] !== true &&
        (attemptsRef.current[row.account] ?? 0) < MAX_QUOTA_LOAD_ATTEMPTS,
    )
    const now = Date.now()
    let slots = MAX_CONCURRENT_QUOTA_LOADS - Object.keys(loadingRef.current).length
    for (const row of pending) {
      if (slots <= 0) break
      const last = lastLoadedRef.current[row.account]
      if (last !== undefined && now - last < AUTO_REFRESH_BACKGROUND_MS) continue
      void refreshQuota(row, last === undefined ? 'initial' : 'background')
      slots--
    }
  }, [accounts, refreshQuota])

  useEffect(() => {
    if (usage.state.kind !== 'ready') return
    pumpQuotaLoads()
    const timer = window.setInterval(pumpQuotaLoads, AUTO_REFRESH_TICK_MS)
    return () => window.clearInterval(timer)
  }, [usage.state, pumpQuotaLoads])

  const filtered = useMemo<readonly UsageAccountView[]>(() => {
    const known = accounts.filter(
      (row) => hasLiveQuota(row) || hasLoadedQuota(row),
    )
    const needle = query.trim().toLowerCase()
    if (needle === '') return known
    return known.filter(
      (row) =>
        row.account.toLowerCase().includes(needle) ||
        row.provider.toLowerCase().includes(needle) ||
        row.state.toLowerCase().includes(needle) ||
        accountLabel(row).toLowerCase().includes(needle),
    )
  }, [accounts, query])
  const liveAccounts = useMemo(
    () => accounts.filter(hasLiveQuota),
    [accounts],
  )

  async function setPin(row: UsageAccountView, pinned: boolean): Promise<void> {
    const provider = providerById[row.provider]
    if (provider === undefined || provider.pool === undefined) return
    const pool = { ...provider.pool, pinnedAccount: pinned ? row.account : '' }
    const result = await pinTask.run(() =>
      api.replaceProvider(
        provider.id,
        providerWriteFrom(provider, providersReady?.generation ?? 0, pool),
      ),
    )
    if (result === undefined) return
    providers.refresh()
  }

  async function removeAccount(): Promise<void> {
    if (confirmingDelete === null) return
    const row = accounts.find((entry) => entry.account === confirmingDelete)
    const result = await deleteTask.run(() =>
      api.deleteAccount(confirmingDelete),
    )
    if (result === undefined) return
    setConfirmingDelete(null)
    if (row !== undefined && pinIndex[row.provider] === row.account) {
      await setPin(row, false)
    }
    usage.refresh()
  }

  function refreshAll(): void {
    if (liveAccounts.length === 0) return
    void refreshAllTask.run(() =>
      Promise.all(liveAccounts.map((row) => refreshQuota(row, 'manual'))),
    )
  }

  return (
    <section className="screen" aria-labelledby="h-usage">
      <div className="screen-head" style={{ '--i': 0 } as CSSProperties}>
        <div>
          <h1 id="h-usage">
            <svg width="19" height="19" className="h-ic">
              <use href="#i-heart" />
            </svg>
            Accounts
          </h1>
          <p className="sub">
            per-account quota windows, the account in use per provider, login
          </p>
        </div>
        <div className="head-actions">
          <Button
            tone="ghost"
            size="sm"
            onClick={() => setAdding(true)}
            disabled={authProviders.length === 0}
            title="Authorize a new account through the provider login flow."
          >
            Add account
          </Button>
          <Button
            tone="ghost"
            size="sm"
            onClick={refreshAll}
            busy={refreshAllTask.running}
            disabled={liveAccounts.length === 0}
          >
            Refresh all
          </Button>
          <SearchInput
            id="usage-search"
            value={query}
            onChange={setQuery}
            placeholder="Filter by account, email, provider, or state"
          />
        </div>
      </div>
      <AsyncBoundary<UsageView>
        state={usage.state}
        loadingLabel="Loading usage…"
        empty={<Empty title="No usage reported." />}
        onRetry={() => usage.refresh()}
      >
        {() => (
          <>
            {adding ? (
              <AddAccountModal
                providers={authProviders}
                onAdded={() => {
                  usage.refresh()
                  providers.refresh()
                }}
                onClose={() => setAdding(false)}
              />
            ) : null}
            {filtered.length === 0 ? (
              <Empty
                title={
                  accounts.length === 0
                    ? 'No accounts yet. Add one to start routing.'
                    : 'No accounts match the filter.'
                }
              />
            ) : (
              <div className="usage-grid">
                {filtered.map((row, index) => (
                  <UsageCard
                    key={row.account}
                    row={row}
                    index={index}
                    loading={quotaLoading[row.account] === true}
                    failed={quotaFailed[row.account]}
                    pinned={pinIndex[row.provider] === row.account}
                    canPin={providerById[row.provider]?.pool !== undefined}
                    onPinToggle={() =>
                      void setPin(row, pinIndex[row.provider] !== row.account)
                    }
                    confirmingDelete={confirmingDelete === row.account}
                    deleteBusy={deleteTask.running || pinTask.running}
                    deleteError={deleteTask.error}
                    onDelete={() => setConfirmingDelete(row.account)}
                    onCancelDelete={() => setConfirmingDelete(null)}
                    onConfirmDelete={() => void removeAccount()}
                  />
                ))}
              </div>
            )}
          </>
        )}
      </AsyncBoundary>
    </section>
  )
}

interface UsageCardProps {
  readonly row: UsageAccountView
  readonly index: number
  readonly loading: boolean
  readonly failed: string | undefined
  readonly pinned: boolean
  readonly canPin: boolean
  readonly onPinToggle: () => void
  readonly confirmingDelete: boolean
  readonly deleteBusy: boolean
  readonly deleteError: Error | null
  readonly onDelete: () => void
  readonly onCancelDelete: () => void
  readonly onConfirmDelete: () => void
}

function UsageCard({
  row,
  index,
  loading,
  failed,
  pinned,
  canPin,
  onPinToggle,
  confirmingDelete,
  deleteBusy,
  deleteError,
  onDelete,
  onCancelDelete,
  onConfirmDelete,
}: UsageCardProps): JSX.Element {

  const windows = quotaWindows(row.quota)
  return (
    <article
      className="panel card usage-card"
      style={{ '--i': index + 1 } as CSSProperties}
    >
      <div className="usage-card-top">
        <div className="usage-card-name">
          <span
            className={`dot dot-${dotTone(row.state)}`}
            aria-hidden="true"
          />
          <span title={row.account}>{accountLabel(row)}</span>
        </div>
        <span className="usage-card-badges">
          <span className={`badge badge--${badgeTone(row.state)}`}>
            {stateLabel(row.state)}
          </span>
          {pinned ? (
            <span
              className="badge badge--ok"
              title="Only this account is used for its provider while pinned."
            >
              Pinned
            </span>
          ) : null}
        </span>
      </div>
      <div className="usage-card-prov">{row.provider}</div>
      {loading ? (
        <div className="usage-windows" aria-label="Loading quota">
          <div className="usage-window">
            <span className="skel skel-line" aria-hidden="true" />
          </div>
          <div className="usage-window">
            <span className="skel skel-line" aria-hidden="true" />
          </div>
        </div>
      ) : windows.length === 0 ? (
        <div className="usage-empty-card">
          {failed ? 'Quota unavailable' : 'Loading…'}
        </div>
      ) : (
        <div className="usage-windows">
          {windows.map((window) => (
            <UsageWindowBar
              key={`${window.label}-${window.windowEnd}`}
              window={window}
            />
          ))}
        </div>
      )}
      <div className="usage-card-foot">
        {confirmingDelete ? (
          <Confirm
            title={`Remove ${accountLabel(row)}?`}
            detail="The account leaves the pool immediately."
            confirmLabel="Remove"
            busy={deleteBusy}
            onCancel={onCancelDelete}
            onConfirm={onConfirmDelete}
          />
        ) : (
          <>
            {canPin ? (
              <Button tone="ghost" size="sm" onClick={onPinToggle}>
                {pinned ? 'Use all accounts' : 'Use only this'}
              </Button>
            ) : null}
            <Button tone="danger" size="sm" onClick={onDelete}>
              Remove
            </Button>
          </>
        )}
      </div>
      {deleteError !== null && !confirmingDelete ? (
        <div className="alert" role="alert">
          <span>{describeError(deleteError)}</span>
        </div>
      ) : null}
      {deleteError !== null && confirmingDelete ? (
        <div className="alert" role="alert">
          <span>{describeError(deleteError)}</span>
        </div>
      ) : null}
    </article>
  )
}

function UsageWindowBar({
  window,
}: {
  readonly window: UsageWindow
}): JSX.Element {
  return (
    <div className="usage-window">
      <span className="usage-window-label">{windowHeader(window.label)}</span>
      <span className="usage-window-val">{leftPercent(window)}%</span>
      <span className="bar">
        <span
          className="bar-fill"
          style={{
            '--w': leftRatio(window),
            background: windowGradient(window),
          } as CSSProperties}
        />
      </span>
      <span className="usage-window-reset">
        {formatReset(window.windowEnd)}
      </span>
    </div>
  )
}
