import { useEffect, useMemo, useState, type CSSProperties } from 'react'
import type { AttemptView, RequestView, RequestsView } from '@prism/contracts'
import { api } from '../api'
import { useAsync } from '../useAsync'
import { AsyncBoundary, Button, Empty, SearchInput, Stat } from '../components/Ui'

const AUTO_REFRESH_ACTIVE_MS = 5_000
const AUTO_REFRESH_BACKGROUND_MS = 30_000
const AUTO_REFRESH_TICK_MS = 1_000

const STATUS_FILTERS = [
  { value: 'all', label: 'all' },
  { value: 'open', label: 'open' },
  { value: 'completed', label: 'completed' },
  { value: 'incomplete', label: 'incomplete' },
  { value: 'failed', label: 'failed' },
] as const

type StatusFilter = (typeof STATUS_FILTERS)[number]['value']

function statusTone(status: RequestView['status']): 'ok' | 'warn' | 'error' | 'info' | 'muted' {
  switch (status) {
    case 'completed':
      return 'ok'
    case 'incomplete':
      return 'warn'
    case 'failed':
      return 'error'
    case 'open':
      return 'info'
    default:
      return 'muted'
  }
}

function formatTime(iso: string): string {
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return '—'
  const year = String(date.getUTCFullYear()).padStart(2, '0')
  const month = String(date.getUTCMonth() + 1).padStart(2, '0')
  const day = String(date.getUTCDate()).padStart(2, '0')
  const hours = String(date.getUTCHours()).padStart(2, '0')
  const minutes = String(date.getUTCMinutes()).padStart(2, '0')
  const seconds = String(date.getUTCSeconds()).padStart(2, '0')
  return `${year}-${month}-${day} ${hours}:${minutes}:${seconds}`
}

function formatDuration(ms: number): string {
  if (ms <= 0) return '—'
  if (ms < 1_000) return `${ms}ms`
  if (ms < 60_000) return `${(ms / 1_000).toFixed(1)}s`
  return `${Math.floor(ms / 60_000)}m${Math.round((ms % 60_000) / 1_000)}s`
}

function winnerOf(request: RequestView): AttemptView | null {
  for (let i = request.attempts.length - 1; i >= 0; i--) {
    if (request.attempts[i]?.outcome === 'succeeded') return request.attempts[i] ?? null
  }
  return null
}

function modelLabel(request: RequestView): string {
  const winner = winnerOf(request)
  if (winner === null || winner.model === request.model) return request.model
  return `${request.model} (${winner.model})`
}

function outcomeTone(outcome: string): 'ok' | 'warn' | 'error' | 'muted' {
  if (outcome === 'succeeded') return 'ok'
  if (outcome === 'rejected' || outcome === 'no_terminal') return 'muted'
  return 'warn'
}

export function LogsView(): JSX.Element {
  const logs = useAsync<RequestsView>(() => api.requests(), [])
  const [query, setQuery] = useState('')
  const [statusFilter, setStatusFilter] = useState<StatusFilter>('all')
  const [expandedSeq, setExpandedSeq] = useState<number | null>(null)
  const [copiedSeq, setCopiedSeq] = useState<number | null>(null)
  const [, setTick] = useState(0)

  useEffect(() => {
    const bump = (): void => setTick((n) => n + 1)
    const timer = window.setInterval(bump, AUTO_REFRESH_TICK_MS)
    return () => window.clearInterval(timer)
  }, [])

  useEffect(() => {
    const refresh = (): void => logs.refresh()
    const timer = window.setInterval(
      refresh,
      document.hidden ? AUTO_REFRESH_BACKGROUND_MS : AUTO_REFRESH_ACTIVE_MS,
    )
    const onVisibility = (): void => {
      window.clearInterval(timer)
      refresh()
    }
    document.addEventListener('visibilitychange', onVisibility)
    return () => {
      window.clearInterval(timer)
      document.removeEventListener('visibilitychange', onVisibility)
    }
  }, [logs])

  const requests = logs.state.kind === 'ready' ? logs.state.value.requests : []
  const dropped = logs.state.kind === 'ready' ? logs.state.value.dropped : 0

  const filtered = useMemo<readonly RequestView[]>(() => {
    const byStatus =
      statusFilter === 'all' ? requests : requests.filter((row) => row.status === statusFilter)
    const needle = query.trim().toLowerCase()
    if (needle === '') return byStatus
    return byStatus.filter(
      (row) =>
        (row.requestId ?? '').toLowerCase().includes(needle) ||
        row.model.toLowerCase().includes(needle) ||
        row.client.toLowerCase().includes(needle) ||
        row.attempts.some((attempt) => attempt.provider.toLowerCase().includes(needle)),
    )
  }, [requests, query, statusFilter])

  const totals = useMemo(() => {
    let errors = 0
    let tokens = 0
    for (const row of requests) {
      if (row.status === 'failed') errors++
      tokens += row.usage.total
    }
    return { errors, tokens }
  }, [requests])

  async function copyRequestId(request: RequestView): Promise<void> {
    if (request.requestId === undefined) return
    try {
      await navigator.clipboard.writeText(request.requestId)
      setCopiedSeq(request.seq)
      window.setTimeout(() => setCopiedSeq(null), 1_500)
    } catch {
      setCopiedSeq(null)
    }
  }

  return (
    <section className="screen" aria-labelledby="h-logs">
      <div className="screen-head" style={{ '--i': 0 } as CSSProperties}>
        <div>
          <h1 id="h-logs">
            <svg width="19" height="19" className="h-ic">
              <use href="#i-logs" />
            </svg>
            Logs
          </h1>
          <p className="sub">
            model requests and failover history, newest first
            {logs.state.kind === 'ready' ? (
              <>
                {' · '}
                <span className="num">{requests.length}</span> in ring
                {dropped > 0 ? (
                  <>
                    {' · ring evicted '}
                    <span className="num">{dropped}</span> older record{dropped === 1 ? '' : 's'}
                  </>
                ) : null}
              </>
            ) : null}
          </p>
        </div>
        <div className="head-actions">
          <SearchInput
            id="logs-search"
            value={query}
            onChange={setQuery}
            placeholder="Filter by request id, model, or provider"
          />
          <div className="seg" role="group" aria-label="Filter requests by status">
            {STATUS_FILTERS.map((entry) => (
              <button
                key={entry.value}
                type="button"
                className="seg-btn"
                aria-pressed={statusFilter === entry.value}
                onClick={() => setStatusFilter(entry.value)}
              >
                {entry.label}
              </button>
            ))}
          </div>
          <Button tone="ghost" size="sm" onClick={() => logs.refresh()}>
            Refresh
          </Button>
        </div>
      </div>
      <AsyncBoundary<RequestsView>
        state={logs.state}
        loadingLabel="Loading requests…"
        onRetry={() => logs.refresh()}
      >
        {(value) =>
          value.requests.length === 0 ? (
            <Empty title="No requests yet.">
              Requests routed through prismd appear here with their full failover trail.
            </Empty>
          ) : (
            <>
              <div className="stats">
                <Stat
                  label="Requests"
                  value={String(requests.length)}
                  hint={`${filtered.length} shown`}
                />
                <Stat
                  label="Failed"
                  value={String(totals.errors)}
                  tone={totals.errors > 0 ? 'error' : 'default'}
                />
                <Stat label="Tokens" value={String(totals.tokens)} />
              </div>
              <section className="panel card" style={{ '--i': 1 } as CSSProperties}>
                <div className="tbl-wrap">
                  <table className="tbl table">
                    <thead>
                      <tr>
                        <th scope="col">Time</th>
                        <th scope="col">Request</th>
                        <th scope="col">Client</th>
                        <th scope="col">Model</th>
                        <th scope="col">Status</th>
                        <th scope="col">Duration</th>
                        <th scope="col">Attempts</th>
                        <th scope="col">Tokens</th>
                      </tr>
                    </thead>
                    <tbody>
                      {filtered.length === 0 ? (
                        <tr>
                          <td colSpan={8}>
                            <Empty title="No requests match the filter." />
                          </td>
                        </tr>
                      ) : (
                        filtered.map((row) => (
                          <RequestRow
                            key={row.seq}
                            request={row}
                            expanded={expandedSeq === row.seq}
                            copied={copiedSeq === row.seq}
                            onToggle={() =>
                              setExpandedSeq(expandedSeq === row.seq ? null : row.seq)
                            }
                            onCopy={() => void copyRequestId(row)}
                          />
                        ))
                      )}
                    </tbody>
                  </table>
                </div>
              </section>
            </>
          )
        }
      </AsyncBoundary>
    </section>
  )
}

interface RequestRowProps {
  readonly request: RequestView
  readonly expanded: boolean
  readonly copied: boolean
  readonly onToggle: () => void
  readonly onCopy: () => void
}

function RequestRow({
  request,
  expanded,
  copied,
  onToggle,
  onCopy,
}: RequestRowProps): JSX.Element {
  const now = Date.now()
  const startedMs = new Date(request.startedAt).getTime()
  const liveMs = Number.isNaN(startedMs) ? 0 : Math.max(0, now - startedMs)
  const duration =
    request.status === 'open' && liveMs > 0 ? formatDuration(liveMs) : formatDuration(request.durationMs)
  return (
    <>
      <tr onClick={onToggle} style={{ cursor: 'pointer' }}>
        <td>
          <span className="num" title={request.startedAt}>
            {formatTime(request.startedAt)}
          </span>
        </td>
        <td className="td-strong">
          {request.requestId ?? '—'}
          {request.session !== undefined && request.session !== '' ? (
            <span className="num" style={{ color: 'var(--fg-subtle)' }}>
              {' '}
              · {request.session}
            </span>
          ) : null}
        </td>
        <td>{request.client}</td>
        <td className="num">{modelLabel(request)}</td>
        <td>
          <span className={`badge badge--${statusTone(request.status)}`}>
            {request.status === 'open' ? (
              <span className="dot dot-ok dot-pulse" aria-hidden="true" style={{ marginRight: 6 }} />
            ) : null}
            {request.status}
          </span>
          {request.reason !== undefined && request.reason !== '' ? (
            <span className="num" style={{ color: 'var(--fg-subtle)' }}>
              {' '}
              {request.reason}
            </span>
          ) : null}
        </td>
        <td>
          <span className="num">{duration}</span>
        </td>
        <td>
          {request.attempts.length > 1 ? (
            <span className="badge badge--info">{request.attempts.length}</span>
          ) : (
            <span className="num">{request.attempts.length}</span>
          )}
        </td>
        <td>
          <span className="num">{request.usage.total}</span>
        </td>
      </tr>
      {expanded ? (
        <tr>
          <td colSpan={8}>
            <div className="kv" style={{ marginTop: 0 }}>
              {request.requestId !== undefined ? (
                <div className="kv-row">
                  <span className="kv-k">Request id</span>
                  <span className="kv-v num">{request.requestId}</span>
                  <Button tone="ghost" size="sm" onClick={onCopy}>
                    {copied ? 'Copied' : 'Copy'}
                  </Button>
                </div>
              ) : null}
              <div className="kv-row">
                <span className="kv-k">Usage</span>
                <span className="kv-v num">
                  in <span className="num">{request.usage.input}</span> · out{' '}
                  <span className="num">{request.usage.output}</span> · cached{' '}
                  <span className="num">{request.usage.cached}</span> · reasoning{' '}
                  <span className="num">{request.usage.reasoning}</span> · total{' '}
                  <span className="num">{request.usage.total}</span>
                </span>
              </div>
              {request.attempts.map((attempt, index) => (
                <div className="kv-row" key={`${request.seq}-${index}`}>
                  <span className="kv-k">Attempt {index + 1}</span>
                  <span className="kv-v">
                    <span className={`badge badge--${outcomeTone(attempt.outcome)}`}>
                      {attempt.outcome}
                    </span>{' '}
                    <span className="num">{attempt.provider}</span>
                    {' · '}
                    <span className="num">{attempt.account}</span>
                    {' · '}
                    <span className="num">{attempt.model}</span>
                    {' · '}
                    <span className="num">{formatDuration(attempt.durationMs)}</span>
                    {attempt.error !== undefined && attempt.error !== '' ? (
                      <>
                        {' · '}
                        <span className="num" style={{ color: 'var(--fg-subtle)' }}>
                          {attempt.error}
                        </span>
                      </>
                    ) : null}
                  </span>
                </div>
              ))}
            </div>
          </td>
        </tr>
      ) : null}
    </>
  )
}
