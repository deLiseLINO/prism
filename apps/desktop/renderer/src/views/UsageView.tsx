import { useCallback, useEffect, useMemo, useRef, useState, type CSSProperties } from 'react'
import type { QuotaView, UsageAccountView, UsageView } from '@prism/contracts'
import { AsyncBoundary, Button, Confirm, Empty, SearchInput } from '../components/Ui'
import { useAsync, useTask, describeError } from '../useAsync'
import { api } from '../api'

const AUTO_REFRESH_ACTIVE_MS = 30_000
const AUTO_REFRESH_BACKGROUND_MS = 300_000
const AUTO_REFRESH_TICK_MS = 1_000
const MAX_CONCURRENT_QUOTA_LOADS = 3
const MAX_QUOTA_LOAD_ATTEMPTS = 3

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

function windowOrder(window: UsageWindow): number {
  const label = window.label.toLowerCase()
  if (label.includes('5 hour')) return 0
  if (label.includes('weekly')) return 1
  return 2
}

function quotaWindows(quota: QuotaView): readonly UsageWindow[] {
  if (quota.source === 'unknown') return []
  if (quota.windows !== undefined && quota.windows.length > 0) {
    return quota.windows
      .slice()
      .sort((left, right) => windowOrder(left) - windowOrder(right))
  }
  return []
}

function windowHeader(label: string): string {
  const lower = label.toLowerCase()
  if (lower.includes('5 hour')) return '5 hour'
  if (lower.includes('weekly')) return 'Weekly'
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

function fillClass(window: UsageWindow): string {
  const usedRatio = 1 - leftRatio(window)
  if (usedRatio > 0.9) return 'f-danger'
  if (usedRatio > 0.7) return 'f-warn'
  return ''
}

function hasLiveQuota(row: UsageAccountView): boolean {
  return KNOWN_QUOTA_PROVIDERS[row.provider] === true
}

function hasLoadedQuota(row: UsageAccountView): boolean {
  return row.quota.source !== 'unknown' && quotaWindows(row.quota).length > 0
}

export function UsagePanel(): JSX.Element {
  const usage = useAsync<UsageView>(() => api.usage(), [])
  const [query, setQuery] = useState('')
  const [quotaLoading, setQuotaLoading] = useState<Record<string, boolean>>({})
  const [quotaFailed, setQuotaFailed] = useState<Record<string, boolean>>({})
  const [confirmingDelete, setConfirmingDelete] = useState<string | null>(null)
  const deleteTask = useTask()
  const loadingRef = useRef<Record<string, boolean>>({})
  const lastLoadedRef = useRef<Record<string, number>>({})
  const attemptsRef = useRef<Record<string, number>>({})

  const accounts = usage.state.kind === 'ready' ? usage.state.value.accounts : []

  const refreshQuota = useCallback(
    async (row: UsageAccountView, silent: boolean): Promise<void> => {
      if (loadingRef.current[row.account] === true) return
      loadingRef.current[row.account] = true
      if (!silent) setQuotaLoading((prev) => ({ ...prev, [row.account]: true }))
      try {
        const response = await api.quota(row.account)
        attemptsRef.current[row.account] = 0
        setQuotaFailed((prev) => {
          if (prev[row.account] !== true) return prev
          const next = { ...prev }
          delete next[row.account]
          return next
        })
        if (response.quota.source !== 'unknown') {
          lastLoadedRef.current[row.account] = Date.now()
        }
        usage.refresh()
      } catch {
        attemptsRef.current[row.account] =
          (attemptsRef.current[row.account] ?? 0) + 1
        const attempts = attemptsRef.current[row.account] ?? 0
        if (attempts >= MAX_QUOTA_LOAD_ATTEMPTS) {
          setQuotaFailed((prev) => ({ ...prev, [row.account]: true }))
        }
      } finally {
        delete loadingRef.current[row.account]
        setQuotaLoading((prev) => {
          if (prev[row.account] !== true) return prev
          const next = { ...prev }
          delete next[row.account]
          return next
        })
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
      if (hasLoadedQuota(row)) {
        const lastLoaded = last === undefined ? now : last
        if (now - lastLoaded < AUTO_REFRESH_BACKGROUND_MS) continue
        lastLoadedRef.current[row.account] = now
        void refreshQuota(row, true)
        slots--
        continue
      }
      lastLoadedRef.current[row.account] = now
      void refreshQuota(row, false)
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

  async function removeAccount(): Promise<void> {
    if (confirmingDelete === null) return
    const result = await deleteTask.run(() =>
      api.deleteAccount(confirmingDelete),
    )
    if (result === undefined) return
    setConfirmingDelete(null)
    usage.refresh()
  }

  return (
    <section className="screen" aria-labelledby="h-usage">
      <div className="screen-head" style={{ '--i': 0 } as CSSProperties}>
        <div>
          <h1 id="h-usage">
            <svg width="19" height="19" className="h-ic">
              <use href="#i-usage" />
            </svg>
            Usage
          </h1>
          <p className="sub">
            per-account used vs limit inside the current quota window
          </p>
        </div>
        <div className="head-actions">
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
        {() =>
          filtered.length === 0 ? (
            <Empty
              title={
                accounts.length === 0
                  ? 'No usage reported.'
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
                  loading={
                    quotaLoading[row.account] === true &&
                    quotaFailed[row.account] !== true
                  }
                  failed={quotaFailed[row.account] === true}
                  confirmingDelete={confirmingDelete === row.account}
                  deleteBusy={deleteTask.running}
                  deleteError={deleteTask.error}
                  onDelete={() => setConfirmingDelete(row.account)}
                  onCancelDelete={() => setConfirmingDelete(null)}
                  onConfirmDelete={() => void removeAccount()}
                />
              ))}
            </div>
          )
        }
      </AsyncBoundary>
    </section>
  )
}

interface UsageCardProps {
  readonly row: UsageAccountView
  readonly index: number
  readonly loading: boolean
  readonly failed: boolean
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
      <div>
        <div className="usage-card-top">
          <div className="usage-card-name">
            <span
              className={`dot dot-${dotTone(row.state)}`}
              aria-hidden="true"
            />
            <span title={row.account}>{accountLabel(row)}</span>
          </div>
          <span className={`badge badge--${badgeTone(row.state)}`}>
            {stateLabel(row.state)}
          </span>
        </div>
        <div className="usage-card-prov">{row.provider}</div>
      </div>
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
          <Button tone="danger" size="sm" onClick={onDelete}>
            Remove
          </Button>
        )}
      </div>
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
          className={`bar-fill ${fillClass(window)}`.trim()}
          style={{ '--w': leftRatio(window) } as CSSProperties}
        />
      </span>
      <span className="usage-window-reset">
        {formatReset(window.windowEnd)}
      </span>
    </div>
  )
}
