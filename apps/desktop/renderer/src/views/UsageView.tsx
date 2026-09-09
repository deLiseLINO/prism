import { useMemo, useState, type CSSProperties } from 'react'
import type { QuotaView, UsageAccountView, UsageView } from '@prism/contracts'
import { AsyncBoundary, Empty, SearchInput } from '../components/Ui'
import { useAsync } from '../useAsync'
import { api } from '../api'

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
  const absolute =
    reset.getDate() === new Date().getDate() &&
    reset.getMonth() === new Date().getMonth() &&
    reset.getFullYear() === new Date().getFullYear()
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
  return [
    {
      label: 'Current usage limit',
      used: quota.used,
      ...(quota.limit === undefined ? {} : { limit: quota.limit }),
      windowEnd: quota.windowEnd,
    },
  ]
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

export function UsagePanel(): JSX.Element {
  const usage = useAsync<UsageView>(() => api.usage(), [])
  const [query, setQuery] = useState('')

  const filtered = useMemo<readonly UsageAccountView[] | null>(() => {
    if (usage.state.kind !== 'ready') return null
    const known = usage.state.value.accounts.filter(
      (row) => row.quota.source !== 'unknown',
    )
    const needle = query.trim().toLowerCase()
    if (needle === '') return known
    return known.filter(
      (row) =>
        row.account.toLowerCase().includes(needle) ||
        row.provider.toLowerCase().includes(needle) ||
        row.state.toLowerCase().includes(needle),
    )
  }, [usage.state, query])

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
            placeholder="Filter by account, provider, or state"
          />
        </div>
      </div>
      <AsyncBoundary<UsageView>
        state={usage.state}
        loadingLabel="Loading usage…"
        empty={<Empty title="No usage reported." />}
        onRetry={() => usage.refresh()}
      >
        {(list) =>
          filtered === null || filtered.length === 0 ? (
            <Empty
              title={
                list.accounts.some((row) => row.quota.source !== 'unknown')
                  ? 'No usage reported.'
                  : 'No accounts match the filter.'
              }
            />
          ) : (
            <div className="usage-grid">
              {filtered.map((row, index) => (
                <UsageCard key={row.account} row={row} index={index} />
              ))}
            </div>
          )
        }
      </AsyncBoundary>
    </section>
  )
}

function UsageCard({
  row,
  index,
}: {
  readonly row: UsageAccountView
  readonly index: number
}): JSX.Element {
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
      {windows.length === 0 ? (
        <div className="usage-empty-card">No usage reported.</div>
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
