import { useMemo, useState } from 'react'
import type { UsageAccountView, UsageView } from '@prism/contracts'
import { AsyncBoundary, Card, Empty, SearchInput, Stack } from '../components/Ui'
import { useAsync } from '../useAsync'
import { api } from '../api'
import { formatWindowEnd } from './AccountsView'

function pct(used: number, limit: number | null): number {
  if (limit === null || limit === 0) return 0
  return Math.max(0, Math.min(100, Math.round((used / limit) * 100)))
}

export function UsagePanel(): JSX.Element {
  const usage = useAsync<UsageView>(() => api.usage(), [])
  const [query, setQuery] = useState('')

  return (
    <Stack gap="normal">
      <AsyncBoundary<UsageView>
        state={usage.state}
        loadingLabel="Loading usage…"
        empty={<Empty title="No usage reported." />}
        onRetry={() => usage.refresh()}
      >
        {(list) => <UsageTable list={list.accounts} query={query} onQuery={setQuery} />}
      </AsyncBoundary>
    </Stack>
  )
}

function UsageTable({ list, query, onQuery }: { readonly list: readonly UsageAccountView[]; readonly query: string; readonly onQuery: (next: string) => void }): JSX.Element {
  const filtered = useMemo(() => {
    const needle = query.trim().toLowerCase()
    if (needle === '') return list
    return list.filter(
      (account) =>
        account.account.toLowerCase().includes(needle) ||
        account.provider.toLowerCase().includes(needle) ||
        account.state.toLowerCase().includes(needle),
    )
  }, [list, query])
  if (list.length === 0) {
    return <Empty title="No usage reported." />
  }
  return (
    <Card
      title="Account usage"
      description="Real per-account quotas decoded by Prism."
      action={
        <p className="meta">{filtered.length} of {list.length} shown</p>
      }
    >
      <div className="toolbar">
        <div className="toolbar__filters">
          <SearchInput id="usage-search" value={query} onChange={onQuery} placeholder="Filter by account, provider, or state" />
        </div>
      </div>
      {filtered.length === 0 ? (
        <Empty title="No usage matches the current filter." />
      ) : (
        <div className="table-wrap">
          <table className="table">
            <thead>
          <tr>
            <th scope="col">Account</th>
            <th scope="col">Provider</th>
            <th scope="col">State</th>
            <th scope="col">Used / limit</th>
            <th scope="col">Window ends</th>
            <th scope="col">Source</th>
          </tr>
        </thead>
        <tbody>
          {filtered.map((account) => {
            const ratio = pct(account.used, account.limit ?? null)
            return (
              <tr key={account.account}>
                <td className="cell-mono">{account.account}</td>
                <td>{account.provider}</td>
                <td>
                  <span className={`badge badge--${badgeTone(account.state)}`}>{account.state}</span>
                </td>
                <td>
                  <div className={`quota ${ratio >= 85 ? 'quota--hot' : ''}`}>
                    <div className="quota__track">
                      <div className="quota__fill" style={{ width: `${ratio}%` }} />
                    </div>
                    <p className="meta">
                      {account.used} / {account.limit == null || account.limit === 0 ? '?' : account.limit}
                    </p>
                  </div>
                </td>
                <td>{formatWindowEnd(account.windowEnd)}</td>
                <td>{account.source}</td>
              </tr>
            )
          })}
        </tbody>
          </table>
        </div>
      )}
    </Card>
  )
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
