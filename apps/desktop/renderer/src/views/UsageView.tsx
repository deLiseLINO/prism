import { useMemo, type CSSProperties } from 'react'
import type { QuotaView, UsageAccountView, UsageView } from '@prism/contracts'
import { AsyncBoundary, Empty } from '../components/Ui'
import { useAsync } from '../useAsync'
import { api } from '../api'
import { formatWindowEnd, quotaCellFromView } from './AccountsView'

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

function stateLabel(state: string): string {
  if (state === 'cooling_down') return 'cooling down'
  if (state === 'needs_reauth') return 'needs reauth'
  if (state === 'soft_avoid') return 'soft avoid'
  return state
}

export function UsagePanel(): JSX.Element {
  const usage = useAsync<UsageView>(() => api.usage(), [])

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
      </div>
      <AsyncBoundary<UsageView>
        state={usage.state}
        loadingLabel="Loading usage…"
        empty={<Empty title="No usage reported." />}
        onRetry={() => usage.refresh()}
      >
        {(list) => <UsageTable list={list.accounts} />}
      </AsyncBoundary>
    </section>
  )
}

function UsageTable({
  list,
}: {
  readonly list: readonly UsageAccountView[]
}): JSX.Element {
  const rows = useMemo(() => {
    if (list.length === 0) return []
    const total = list.reduce((sum, row) => sum + row.quota.used, 0)
    return list.map((row) => {
      const used = row.quota.used
      const share = total === 0 ? 0 : Math.max(0, Math.min(1, used / total))
      return { row, share }
    })
  }, [list])
  if (list.length === 0) {
    return <Empty title="No usage reported." />
  }
  return (
    <section className="panel card" style={{ '--i': 1 } as CSSProperties}>
      <div className="tbl-wrap">
        <table className="tbl table">
          <thead>
            <tr>
              <th scope="col">Account</th>
              <th scope="col">Provider</th>
              <th scope="col">State</th>
              <th scope="col">Used / limit</th>
              <th scope="col">Share</th>
              <th scope="col">Window ends</th>
              <th scope="col">Source</th>
            </tr>
          </thead>
          <tbody>
            {rows.map(({ row, share }) => (
              <tr key={row.account}>
                <td className="td-strong">{row.account}</td>
                <td>{row.provider}</td>
                <td>
                  <span className={`badge badge--${badgeTone(row.state)}`}>
                    {stateLabel(row.state)}
                  </span>
                </td>
                <td>
                  <QuotaWindows quota={row.quota} />
                </td>
                <td style={{ minWidth: 130 }}>
                  <span className="bar">
                    <span
                      className="bar-fill"
                      style={{ '--w': share } as CSSProperties}
                    />
                  </span>
                </td>
                <td>
                  <span className="num">{formatWindowEnd(row.quota.windowEnd)}</span>
                </td>
                <td>{row.quota.source}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  )
}

function QuotaWindows({ quota }: { readonly quota: QuotaView }): JSX.Element {
  const cell = quotaCellFromView(quota)
  if (cell.kind === 'unavailable') {
    return (
      <span className="num" style={{ color: 'var(--fg-subtle)' }}>
        no usage reported
      </span>
    )
  }
  const windows =
    cell.windows.length > 0
      ? cell.windows
      : [
          {
            label: 'Current usage limit',
            used: cell.used,
            ...(cell.limit === undefined ? {} : { limit: cell.limit }),
            windowEnd: cell.windowEnd,
          },
        ]
  return (
    <span className="quota-windows">
      {windows.map((window) => {
        const limit = window.limit ?? 0
        const ratio =
          limit === 0 ? 0 : Math.max(0, Math.min(1, window.used / limit))
        const fill = ratio > 0.9 ? 'f-danger' : ratio > 0.7 ? 'f-warn' : ''
        return (
          <span
            className="quota-window"
            key={`${window.label}-${window.windowEnd}`}
          >
            <span className="num">
              {window.used}/{limit === 0 ? '?' : limit}
            </span>
            <span className="bar">
              <span
                className={`bar-fill ${fill}`.trim()}
                style={{ '--w': ratio } as CSSProperties}
              />
            </span>
            <span className="quota-window-label">{window.label}</span>
          </span>
        )
      })}
    </span>
  )
}
