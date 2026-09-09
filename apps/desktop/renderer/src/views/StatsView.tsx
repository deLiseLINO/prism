import { useState, type CSSProperties } from 'react'
import type {
  StatsModelView,
  StatsProviderView,
  StatsRange,
  StatsResponseView,
} from '@prism/contracts'
import { AsyncBoundary, Empty, Stat } from '../components/Ui'
import { useAsync, type AsyncState } from '../useAsync'
import { api } from '../api'

export const STATS_RANGES: readonly {
  readonly value: StatsRange
  readonly label: string
}[] = [
  { value: '1h', label: 'Last hour' },
  { value: '24h', label: 'Last 24 hours' },
  { value: '7d', label: 'Last 7 days' },
  { value: '30d', label: 'Last 30 days' },
  { value: 'all', label: 'All time' },
]

const MAX_MODELS = 10

export function barRatio(value: number, max: number): number {
  if (max <= 0) return 0
  return Math.max(0, Math.min(1, value / max))
}

function fmt(n: number): string {
  return n.toLocaleString('en-US')
}

function counts(row: { requests: number; completed: number; failed: number }): string {
  return `${fmt(row.requests)} req · ${fmt(row.completed)} ok · ${fmt(row.failed)} fail`
}

function coverage(o: { measured: number; requests: number }): string {
  if (o.requests <= 0) return '0%'
  return `${Math.round((o.measured / o.requests) * 100)}%`
}

function StatsRow({
  name,
  tag,
  row,
  max,
}: {
  readonly name: string
  readonly tag?: string
  readonly row: StatsProviderView | StatsModelView
  readonly max: number
}): JSX.Element {
  return (
    <div className="stats-row">
      <div className="stats-row-main">
        <div className="stats-row-name">
          <span title={name}>{name}</span>
          {tag !== undefined ? <span className="stats-row-tag">{tag}</span> : null}
        </div>
        <span className="num">{fmt(row.total_tokens)}</span>
        <span className="bar">
          <span
            className="bar-fill"
            style={{ '--w': barRatio(row.total_tokens, max) } as CSSProperties}
          />
        </span>
        <span className="sub">{counts(row)}</span>
      </div>
    </div>
  )
}

function StatsPanel({
  title,
  sub,
  rows,
  max,
  nameOf,
  tagOf,
  loadingLabel,
  onRetry,
  state,
}: {
  readonly title: string
  readonly sub: string
  readonly rows: readonly (StatsProviderView | StatsModelView)[]
  readonly max: number
  readonly nameOf: (row: StatsProviderView | StatsModelView) => string
  readonly tagOf?: (row: StatsModelView) => string
  readonly loadingLabel: string
  readonly onRetry: () => void
  readonly state: AsyncState<StatsResponseView>
}): JSX.Element {
  return (
    <section className="panel card panel-pad" style={{ '--i': 2 } as CSSProperties}>
      <div className="panel-title">{title}</div>
      <p className="panel-sub">{sub}</p>
      <div className="divide" style={{ marginTop: 10 }}>
        <AsyncBoundary<StatsResponseView>
          state={state}
          loadingLabel={loadingLabel}
          empty={<Empty title="No stats yet." />}
          onRetry={onRetry}
        >
          {() =>
            rows.length === 0 ? (
              <Empty title="No stats yet." />
            ) : (
              rows.map((row) => (
                <StatsRow
                  key={nameOf(row)}
                  name={nameOf(row)}
                  tag={tagOf === undefined ? undefined : tagOf(row as StatsModelView)}
                  row={row}
                  max={max}
                />
              ))
            )
          }
        </AsyncBoundary>
      </div>
    </section>
  )
}

export function StatsView(): JSX.Element {
  const [range, setRange] = useState<StatsRange>('24h')
  const stats = useAsync<StatsResponseView>(() => api.stats(range), [range])
  const value = stats.state.kind === 'ready' ? stats.state.value : null
  const overview = value?.overview
  const providers = value?.providers ?? []
  const models = (value?.models ?? []).slice(0, MAX_MODELS)
  const maxTotal = Math.max(
    0,
    ...providers.map((row) => row.total_tokens),
    ...models.map((row) => row.total_tokens),
  )

  return (
    <section className="screen" aria-labelledby="h-stats">
      <div className="screen-head" style={{ '--i': 0 } as CSSProperties}>
        <div>
          <h1 id="h-stats">
            <svg width="19" height="19" className="h-ic">
              <use href="#i-usage" />
            </svg>
            Stats
          </h1>
          <p className="sub">
            request and token statistics across providers and models
          </p>
        </div>
        <div className="head-actions">
          <div className="seg" role="group" aria-label="Stats time range">
            {STATS_RANGES.map((entry) => (
              <button
                key={entry.value}
                type="button"
                className="seg-btn"
                aria-pressed={range === entry.value}
                onClick={() => setRange(entry.value)}
              >
                {entry.label}
              </button>
            ))}
          </div>
        </div>
      </div>
      <section className="panel card stats" style={{ '--i': 1 } as CSSProperties}>
        {stats.state.kind === 'error' ? (
          <div className="state--error">
            <div className="state__headline">Stats unavailable</div>
            <div className="state__detail">{stats.state.error.message}</div>
          </div>
        ) : overview === undefined ? (
          <div className="state--loading">
            <span className="skel skel-line" aria-hidden="true" />
            <span className="skel skel-line" aria-hidden="true" />
          </div>
        ) : (
          <>
            <Stat
              label="Requests"
              value={fmt(overview.requests)}
              hint={`${fmt(overview.completed)} completed · ${fmt(overview.failed)} failed`}
            />
            <Stat label="Tokens In" value={fmt(overview.input_tokens)} />
            <Stat label="Tokens Out" value={fmt(overview.output_tokens)} />
            <Stat
              label="Cached"
              value={fmt(overview.cached_tokens)}
              hint={`${fmt(overview.reasoning_tokens)} reasoning tokens`}
            />
            <Stat
              label="Total Tokens"
              value={fmt(overview.total_tokens)}
              tone={overview.failed > overview.completed ? 'warn' : 'default'}
            />
            <Stat
              label="Coverage"
              value={`${fmt(overview.measured)}/${fmt(overview.requests)}`}
              hint={`${coverage(overview)} of requests with reported usage`}
            />
          </>
        )}
      </section>
      <StatsPanel
        title="Providers"
        sub="requests, completions, failures and total tokens per provider"
        rows={providers}
        max={maxTotal}
        nameOf={(row) => row.provider}
        loadingLabel="Loading stats…"
        onRetry={stats.refresh}
        state={stats.state}
      />
      <StatsPanel
        title="Models"
        sub="top models by total tokens in the selected range"
        rows={models}
        max={maxTotal}
        nameOf={(row) => (row as StatsModelView).model}
        tagOf={(row) => row.provider}
        loadingLabel="Loading stats…"
        onRetry={stats.refresh}
        state={stats.state}
      />
    </section>
  )
}
