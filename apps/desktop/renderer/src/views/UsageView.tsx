import { useMemo, type CSSProperties } from 'react'
import type { UsageAccountView, UsageView } from '@prism/contracts'
import { AsyncBoundary, Empty } from '../components/Ui'
import { useAsync } from '../useAsync'
import { api } from '../api'
import { formatWindowEnd } from './AccountsView'

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
    const total = list.reduce((sum, row) => sum + row.used, 0)
    return list.map((row) => {
      const share = total === 0 ? 0 : Math.max(0, Math.min(1, row.used / total))
      const ratio =
        row.limit == null || row.limit === 0
          ? 0
          : Math.max(0, Math.min(1, row.used / row.limit))
      const fill = ratio > 0.9 ? 'f-danger' : ratio > 0.7 ? 'f-warn' : ''
      return { row, share, fill }
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
            {rows.map(({ row, share, fill }) => (
              <tr key={row.account}>
                <td className="td-strong">{row.account}</td>
                <td>{row.provider}</td>
                <td>
                  <span className={`badge badge--${badgeTone(row.state)}`}>
                    {stateLabel(row.state)}
                  </span>
                </td>
                <td>
                  <span className="num">
                    {row.used} /{' '}
                    {row.limit == null || row.limit === 0
                      ? 'no limit'
                      : row.limit}
                  </span>
                </td>
                <td style={{ minWidth: 130 }}>
                  <span className="bar">
                    <span
                      className={`bar-fill ${fill}`.trim()}
                      style={{ '--w': share } as CSSProperties}
                    />
                  </span>
                </td>
                <td>
                  <span className="num">{formatWindowEnd(row.windowEnd)}</span>
                </td>
                <td>{row.source}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  )
}
