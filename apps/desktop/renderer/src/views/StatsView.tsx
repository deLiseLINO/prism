import { useEffect, useState, type CSSProperties } from 'react'
import type { StatsModelView, StatsRange } from '@prism/contracts'
import { AsyncBoundary, Empty } from '../components/Ui'
import { Metric, PageHeader, Section } from '../components/Patterns'
import { loadAcross, MachineFailures, MachineLegend, MachineMenu, scopeKey, StackBar, useMachineScope } from '../components/MachineScope'
import { useAsync, type AsyncState } from '../useAsync'
import { api } from '../api'
import { mergeStats, scopeCount, type Machine, type MergedModel, type MergedProvider, type MergedStats } from '../machines'

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
const RANGE_STORAGE_KEY = 'prism-stats-range'
const AUTO_REFRESH_ACTIVE_MS = 5_000
const AUTO_REFRESH_BACKGROUND_MS = 30_000

export function isStatsRange(value: unknown): value is StatsRange {
  return STATS_RANGES.some((entry) => entry.value === value)
}

export function storedRange(): StatsRange {
  try {
    const value = window.localStorage.getItem(RANGE_STORAGE_KEY)
    return isStatsRange(value) ? value : '24h'
  } catch {
    return '24h'
  }
}

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
  stack,
}: {
  readonly name: string
  readonly tag?: string
  readonly row: MergedProvider | MergedModel
  readonly max: number
  readonly stack: readonly Machine[] | null
}): JSX.Element {
  return (
    <div className="drow">
      <div className="drow__name">
        <span title={name}>{name}</span>
        {tag !== undefined ? <span className="drow__tag">{tag}</span> : null}
      </div>
      <span className="num drow__tok">{fmt(row.total_tokens)}</span>
      {stack !== null ? (
        <StackBar byMachine={row.byMachine} machines={stack} max={max} />
      ) : (
        <span className="bar">
          <span
            className="bar-fill"
            style={{ '--w': barRatio(row.total_tokens, max) } as CSSProperties}
          />
        </span>
      )}
      <span className="drow__counts">{counts(row)}</span>
    </div>
  )
}

function StatsPanel({
  title,
  rows,
  max,
  nameOf,
  tagOf,
  loadingLabel,
  onRetry,
  state,
  stack,
}: {
  readonly title: string
  readonly rows: readonly (MergedProvider | MergedModel)[]
  readonly max: number
  readonly nameOf: (row: MergedProvider | MergedModel) => string
  readonly tagOf?: (row: StatsModelView) => string
  readonly loadingLabel: string
  readonly onRetry: () => void
  readonly state: AsyncState<MergedStats>
  // Machines in stack order when bars split by machine; null draws plain bars.
  readonly stack: readonly Machine[] | null
}): JSX.Element {
  return (
    <Section title={title} action={stack !== null ? <MachineLegend machines={stack} /> : undefined}>
      <AsyncBoundary<MergedStats>
        state={state}
        loadingLabel={loadingLabel}
        empty={<Empty icon="#i-usage" title="No stats yet." />}
        onRetry={onRetry}
      >
        {() =>
          rows.length === 0 ? (
            <div className="list"><Empty icon="#i-usage" inline title="No stats yet.">Requests routed through the daemon show up here.</Empty></div>
          ) : (
            <div className="list">
              <div className="drow drow--head" aria-hidden="true">
                <span>{title === 'Models' ? 'Model' : 'Provider'}</span>
                <span>Tokens</span>
                <span>Share</span>
                <span>Requests</span>
              </div>
              {rows.map((row) => {
                const tag = tagOf === undefined ? undefined : tagOf(row as StatsModelView)
                return (
                  <StatsRow
                    key={`${nameOf(row)} ${tag ?? ''}`}
                    name={nameOf(row)}
                    tag={tag}
                    row={row}
                    max={max}
                    stack={stack}
                  />
                )
              })}
            </div>
          )
        }
      </AsyncBoundary>
    </Section>
  )
}

export function StatsView(): JSX.Element {
  const [range, setRange] = useState<StatsRange>(storedRange)
  const machineScope = useMachineScope()
  const stats = useAsync<MergedStats>(
    () => loadAcross({ state: machineScope, load: (host) => api.stats(range, host), merge: mergeStats }),
    [range, scopeKey(machineScope)],
  )

  useEffect(() => {
    try {
      window.localStorage.setItem(RANGE_STORAGE_KEY, range)
    } catch {
      return
    }
  }, [range])
  const value = stats.state.kind === 'ready' ? stats.state.value : null
  const overview = value?.overview
  const providers = value?.providers ?? []
  const models = (value?.models ?? []).slice(0, MAX_MODELS)
  const machines = machineScope.kind === 'scoped' ? machineScope.machines : []
  const stack = machineScope.kind === 'scoped' && machineScope.scope.kind === 'all' ? machineScope.machines : null

  useEffect(() => {
    const refresh = (): void => stats.refresh()
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
  }, [stats])

  const maxTotal = Math.max(
    0,
    ...providers.map((row) => row.total_tokens),
    ...models.map((row) => row.total_tokens),
  )

  return (
    <section className="screen" aria-labelledby="h-stats">
      <PageHeader
        id="h-stats"
        icon="#i-usage"
        title="Stats"
        actions={
          <>
            {machineScope.kind === 'scoped' ? (
              <MachineMenu
                machines={machineScope.machines}
                scope={machineScope.scope}
                onChange={machineScope.setScope}
                countOf={(scope) => (value === null ? undefined : scopeCount(scope, value.counts, machineScope.machines))}
              />
            ) : null}
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
          </>
        }
      />
      {value !== null ? <MachineFailures failures={value.failures} machines={machines} /> : null}
      {stats.state.kind === 'error' ? (
        <div className="state--error" role="alert">
          <div className="state__headline">Stats unavailable</div>
          <div className="state__detail">{stats.state.error.message}</div>
        </div>
      ) : overview === undefined ? (
        <div className="metrics" role="status" aria-busy="true">
          {Array.from({ length: 6 }, (_, index) => (
            <div className="metric" key={index}>
              <span className="skel skel-line" aria-hidden="true" />
              <span className="skel skel-num" aria-hidden="true" />
            </div>
          ))}
        </div>
      ) : (
        <div className="metrics">
          <Metric
            label="Requests"
            value={fmt(overview.requests)}
            hint={`${fmt(overview.completed)} completed · ${fmt(overview.failed)} failed`}
          />
          <Metric label="Tokens In" value={fmt(overview.input_tokens)} />
          <Metric label="Tokens Out" value={fmt(overview.output_tokens)} />
          <Metric
            label="Cached"
            value={fmt(overview.cached_tokens)}
            hint={`${fmt(overview.reasoning_tokens)} reasoning tokens`}
          />
          <Metric
            label="Total Tokens"
            value={fmt(overview.total_tokens)}
            tone={overview.failed > overview.completed ? 'warn' : 'default'}
          />
          <Metric
            label="Coverage"
            value={`${fmt(overview.measured)}/${fmt(overview.requests)}`}
            hint={coverage(overview)}
          />
        </div>
      )}
      <StatsPanel
        title="Providers"
        rows={providers}
        max={maxTotal}
        nameOf={(row) => row.provider}
        loadingLabel="Loading stats…"
        onRetry={stats.refresh}
        state={stats.state}
        stack={stack}
      />
      <StatsPanel
        title="Models"
        rows={models}
        max={maxTotal}
        nameOf={(row) => (row as StatsModelView).model}
        tagOf={(row) => row.provider}
        loadingLabel="Loading stats…"
        onRetry={stats.refresh}
        state={stats.state}
        stack={stack}
      />
    </section>
  )
}
