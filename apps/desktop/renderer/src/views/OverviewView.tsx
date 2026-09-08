import type { CSSProperties } from 'react'
import type {
  AccountView,
  AccountsView,
  DaemonStatus,
  ProvidersView,
  UsageView,
} from '@prism/contracts'
import { AsyncBoundary, Button, Empty } from '../components/Ui'
import { useAsync, type AsyncState } from '../useAsync'
import { api } from '../api'
import { navigateTo } from '../routing'

interface StatContent {
  readonly value: string
  readonly hint: string
  readonly small?: boolean
}

interface StatCellProps<T> {
  readonly label: string
  readonly state: AsyncState<T>
  readonly render: (value: T) => StatContent
}

function formatClock(iso: string): string {
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return iso
  return `${date.toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit', timeZone: 'UTC' })}Z`
}

function badgeTone(state: string): 'ok' | 'warn' | 'error' | 'muted' {
  switch (state) {
    case 'active':
      return 'ok'
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

function daemonChip(status: AsyncState<DaemonStatus>): {
  readonly tone: 'ok' | 'warn' | 'danger' | 'muted'
  readonly label: string
  readonly pulse: boolean
} {
  if (status.kind !== 'ready')
    return { tone: 'muted', label: 'checking…', pulse: false }
  const state = status.value.state
  if (state === 'ready')
    return { tone: 'ok', label: 'operational', pulse: true }
  if (state === 'failed')
    return { tone: 'danger', label: 'failed', pulse: false }
  if (state === 'idle' || state === 'stopped' || state === 'quitting')
    return { tone: 'muted', label: state, pulse: false }
  return { tone: 'warn', label: state, pulse: false }
}

function daemonSub(status: AsyncState<DaemonStatus>): JSX.Element {
  if (status.kind === 'error')
    return <>daemon status unavailable, retry from the Daemon view</>
  if (status.kind !== 'ready') return <>reading daemon status…</>
  const value = status.value
  if (value.state !== 'ready') {
    return (
      <>
        daemon {value.state}, attempt{' '}
        <span className="num">{value.attempt}</span>
      </>
    )
  }
  return (
    <>
      daemon ready
      {value.endpoint === null ? (
        ''
      ) : (
        <>
          {' '}
          at <span className="num">{value.endpoint}</span>
        </>
      )}
      , attempt <span className="num">{value.attempt}</span>
      {value.startedAt === null ? (
        ''
      ) : (
        <>
          , up since <span className="num">{formatClock(value.startedAt)}</span>
        </>
      )}
    </>
  )
}

function providerMix(list: UsageView): StatContent {
  const totals = new Map<string, number>()
  for (const row of list.accounts) {
    const used = row.quota.used
    totals.set(row.provider, (totals.get(row.provider) ?? 0) + used)
  }
  const sum = [...totals.values()].reduce((left, right) => left + right, 0)
  if (sum === 0)
    return { value: 'no usage yet', hint: 'by quota used', small: true }
  const parts = [...totals.entries()]
    .sort((left, right) => right[1] - left[1])
    .map(([provider, used]) => `${provider} ${Math.round((used / sum) * 100)}%`)
  return { value: parts.join(', '), hint: 'by quota used', small: true }
}

function StatCell<T>({ label, state, render }: StatCellProps<T>): JSX.Element {
  const content = state.kind === 'ready' ? render(state.value) : null
  const failed = state.kind === 'error'
  return (
    <div className="stat">
      <div className="label">{label}</div>
      {content === null ? (
        failed ? (
          <>
            <div className="value num">—</div>
            <div className="hint">unavailable</div>
          </>
        ) : (
          <>
            <div className="value">
              <span className="skel skel-num" aria-hidden="true" />
            </div>
            <div className="hint">
              <span className="skel skel-line" aria-hidden="true" />
            </div>
          </>
        )
      ) : (
        <>
          <div
            className="value num"
            style={
              content.small === true
                ? { fontSize: '12.5px', lineHeight: 1.7, letterSpacing: 0 }
                : undefined
            }
          >
            {content.value}
          </div>
          <div className="hint">{content.hint}</div>
        </>
      )}
    </div>
  )
}

function attnDetail(account: AccountView): string {
  if (account.state === 'needs_reauth')
    return 'needs reauth, start the flow from the Auth view'
  if (account.state === 'cooling_down') {
    const quota =
      account.quota.limit == null
        ? `${account.quota.used} used`
        : `${account.quota.used}/${account.quota.limit}`
    const until =
      account.cooldownUntil === undefined
        ? '—'
        : formatClock(account.cooldownUntil)
    return `cooling down until ${until}, quota at ${quota}`
  }
  if (account.state === 'soft_avoid') {
    const until =
      account.softAvoidUntil === undefined
        ? '—'
        : formatClock(account.softAvoidUntil)
    return `soft avoid until ${until}`
  }
  if (account.state === 'paused') return 'paused manually'
  return 'state unknown, check the daemon logs'
}

function AttnRow({ account }: { readonly account: AccountView }): JSX.Element {
  return (
    <div className="attn">
      <span
        className={`dot dot-${dotTone(account.state)}`}
        aria-hidden="true"
      />
      <div className="attn-main">
        <div className="attn-name">{account.id}</div>
        <div className="sub">{attnDetail(account)}</div>
      </div>
      <span className={`badge badge--${badgeTone(account.state)}`}>
        {stateLabel(account.state)}
      </span>
      <span className="num">{account.priority}</span>
      <Button tone="ghost" size="sm" onClick={() => navigateTo('accounts')}>
        Open accounts
      </Button>
    </div>
  )
}

export function OverviewView(): JSX.Element {
  const status = useAsync<DaemonStatus>(() => window.prism.daemon.status(), [])
  const accounts = useAsync<AccountsView>(() => api.accounts(), [])
  const providers = useAsync<ProvidersView>(() => api.providers(), [])
  const usage = useAsync<UsageView>(() => api.usage(), [])

  const chip = daemonChip(status.state)
  const busy = [
    accounts.state,
    providers.state,
    usage.state,
  ].some((entry) => entry.kind !== 'ready')

  return (
    <section className="screen" aria-labelledby="h-overview">
      <div className="screen-head" style={{ '--i': 0 } as CSSProperties}>
        <div>
          <h1 id="h-overview">
            <svg width="19" height="19" className="h-ic">
              <use href="#i-gauge" />
            </svg>
            Overview
          </h1>
          <p className="sub">{daemonSub(status.state)}</p>
        </div>
        <div className="head-actions">
          <span className={`chip chip-${chip.tone}`}>
            <span
              className={`dot dot-${chip.tone}${chip.pulse ? ' dot-pulse' : ''}`}
              aria-hidden="true"
            />
            {chip.label}
          </span>
        </div>
      </div>
      <section
        className="panel card stats"
        style={{ '--i': 1 } as CSSProperties}
        aria-busy={busy}
      >
        <StatCell<AccountsView>
          label="Accounts"
          state={accounts.state}
          render={(list) => {
            const total = list.accounts.length
            const active = list.accounts.filter(
              (account) => account.state === 'active',
            ).length
            return {
              value: `${active} / ${total}`,
              hint: `${total - active} held back`,
            }
          }}
        />
        <StatCell<AccountsView>
          label="In-flight"
          state={accounts.state}
          render={(list) => {
            const sum = list.accounts.reduce(
              (total, account) => total + account.inFlight,
              0,
            )
            const spread = list.accounts.filter(
              (account) => account.inFlight > 0,
            ).length
            return {
              value: String(sum),
              hint: `spread over ${spread} accounts`,
            }
          }}
        />
        <StatCell<ProvidersView>
          label="Providers"
          state={providers.state}
          render={(list) => {
            const total = list.providers.length
            const enabled = list.providers.filter(
              (provider) => provider.enabled !== false,
            ).length
            return {
              value: `${enabled} / ${total}`,
              hint: `${total - enabled} disabled`,
            }
          }}
        />
        <StatCell<UsageView>
          label="Provider mix"
          state={usage.state}
          render={providerMix}
        />
      </section>
      <section
        className="panel card panel-pad"
        style={{ '--i': 2 } as CSSProperties}
      >
        <div className="panel-title">Accounts needing attention</div>
        <p className="panel-sub">
          reauth, cooldowns and manual pauses across the pool
        </p>
        <div className="divide" style={{ marginTop: 10 }}>
          <AsyncBoundary<AccountsView>
            state={accounts.state}
            loadingLabel="Loading accounts…"
            empty={<Empty title="No accounts configured." />}
            onRetry={accounts.refresh}
          >
            {(list) => {
              const flagged = list.accounts
                .filter((account) => account.state !== 'active')
                .sort(
                  (left, right) =>
                    right.priority - left.priority ||
                    left.id.localeCompare(right.id),
                )
              if (flagged.length === 0)
                return <Empty title="All accounts active." />
              return flagged.map((account) => (
                <AttnRow key={account.id} account={account} />
              ))
            }}
          </AsyncBoundary>
        </div>
      </section>
    </section>
  )
}
