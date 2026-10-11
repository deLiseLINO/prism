import { useEffect, useMemo, useState } from 'react'
import type { AttemptView, RequestView, RequestsView } from '@prism/contracts'
import { api } from '../api'
import { useAsync } from '../useAsync'
import { AsyncBoundary, Button, Empty, SearchInput } from '../components/Ui'
import { Metric, PageHeader, Toolbar, ToolbarSpacer } from '../components/Patterns'

const AUTO_REFRESH_ACTIVE_MS = 1_000
const AUTO_REFRESH_BACKGROUND_MS = 30_000
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
      return 'muted'
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
  if (Number.isNaN(date.getTime())) return '-'
  const year = String(date.getUTCFullYear()).padStart(2, '0')
  const month = String(date.getUTCMonth() + 1).padStart(2, '0')
  const day = String(date.getUTCDate()).padStart(2, '0')
  const hours = String(date.getUTCHours()).padStart(2, '0')
  const minutes = String(date.getUTCMinutes()).padStart(2, '0')
  const seconds = String(date.getUTCSeconds()).padStart(2, '0')
  return `${year}-${month}-${day} ${hours}:${minutes}:${seconds}`
}

function formatDuration(ms: number): string {
  if (ms <= 0) return '-'
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
    <section className="screen logs-page" aria-labelledby="h-logs">
      <PageHeader
        id="h-logs"
        icon="#i-logs"
        title="Logs"
        actions={
          <Button tone="ghost" onClick={() => logs.refresh()}>
            Refresh
          </Button>
        }
      />
      <AsyncBoundary<RequestsView>
        state={logs.state}
        loadingLabel="Loading requests…"
        onRetry={() => logs.refresh()}
      >
        {(value) =>
          value.requests.length === 0 ? (
            <Empty icon="#i-logs" title="No requests yet.">
              Requests routed through the daemon appear here with their full failover trail.
            </Empty>
          ) : (
            <>
              <div className="metrics">
                <Metric
                  label="Requests"
                  value={String(requests.length)}
                  hint={`${filtered.length} shown`}
                />
                <Metric
                  label="Failed"
                  value={String(totals.errors)}
                  tone={totals.errors > 0 ? 'error' : 'default'}
                />
                <Metric label="Tokens" value={String(totals.tokens)} />
              </div>
              <Toolbar label="Request filters">
                <SearchInput
                  id="logs-search"
                  value={query}
                  onChange={setQuery}
                  placeholder="Filter by request id, model, or provider"
                  hotkey
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
                <ToolbarSpacer />
                <span className="toolbar__count num">
                  {filtered.length} / {requests.length}
                  {dropped > 0 ? ` · ${dropped} evicted` : ''}
                </span>
              </Toolbar>
              <section className="logs-wrap">
                <table className="logs-tbl">
                  <colgroup>
                    <col style={{ width: 168 }} />
                    <col />
                    <col style={{ width: 88 }} />
                    <col style={{ width: 170 }} />
                    <col style={{ width: 118 }} />
                    <col style={{ width: 76 }} />
                    <col style={{ width: 78 }} />
                    <col style={{ width: 78 }} />
                  </colgroup>
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
                          <Empty inline icon="#i-logs" title="No requests match the filter." />
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
      <tr
        className="logs-row"
        tabIndex={0}
        aria-expanded={expanded}
        onClick={onToggle}
        onKeyDown={(event) => {
          if (event.key !== 'Enter' && event.key !== ' ') return
          event.preventDefault()
          onToggle()
        }}
      >
        <td>
          <span className="num" title={request.startedAt}>
            {formatTime(request.startedAt)}
          </span>
        </td>
        <td className="td-strong" title={request.requestId ?? ''}>
          <span className="num">{request.requestId ?? '-'}</span>
          {request.session !== undefined && request.session !== '' ? (
            <span className="num logs-sub"> · {request.session}</span>
          ) : null}
        </td>
        <td title={request.client}>{request.client}</td>
        <td className="num" title={modelLabel(request)}>{modelLabel(request)}</td>
        <td title={request.reason ?? ''}>
          <span className={`badge badge--${statusTone(request.status)}`}>
            {request.status === 'open' ? (
              <span className="dot dot-ok dot-pulse" aria-hidden="true" />
            ) : null}
            {request.status}
          </span>
          {request.reason !== undefined && request.reason !== '' ? (
            <span className="num logs-reason">{request.reason}</span>
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
          <td colSpan={8} className="logs-detail">
            <div className="kv">
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
                        <span className="num logs-sub">
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
