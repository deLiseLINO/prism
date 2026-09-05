import type { AccountsView, CombosView, IntegrationStatus, ProvidersView, RoutesView, UsageView } from '@prism/contracts'
import type { DaemonStatus } from '@prism/contracts'
import { AsyncBoundary, Button, Card, Empty, Stack, Stat } from '../components/Ui'
import { useAsync } from '../useAsync'
import { api } from '../api'
import { navigateTo } from '../routing'

function daemonTone(status: DaemonStatus): 'ok' | 'warn' | 'error' {
  if (status.state === 'ready') return 'ok'
  if (status.state === 'failed') return 'error'
  return 'warn'
}

function daemonHeadline(status: DaemonStatus): string {
  if (status.state === 'ready') return status.endpoint === null ? 'Daemon ready' : `Daemon ready on ${status.endpoint}`
  if (status.state === 'failed') return 'Daemon failed'
  return `Daemon ${status.state}`
}

function formatTimestamp(iso: string): string {
  const date = new Date(iso)
  return Number.isNaN(date.getTime()) ? iso : date.toLocaleString()
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

export function OverviewView(): JSX.Element {
  const status = useAsync<DaemonStatus>(() => window.prism.daemon.status(), [])
  const accounts = useAsync<AccountsView>(() => api.accounts(), [])
  const providers = useAsync<ProvidersView>(() => api.providers(), [])
  const combos = useAsync<CombosView>(() => api.combos(), [])
  const routes = useAsync<RoutesView>(() => api.routes(), [])
  const usage = useAsync<UsageView>(() => api.usage(), [])
  const integrations = useAsync<readonly IntegrationStatus[]>(() => window.prism.integrations.status(), [])

  return (
    <Stack gap="normal">
      <AsyncBoundary<DaemonStatus>
        state={status.state}
        loadingLabel="Reading daemon status…"
        empty={<p>no status yet.</p>}
        onRetry={status.refresh}
      >
        {(current) => (
          <Card
            title={daemonHeadline(current)}
            description={
              current.startedAt === null ? 'Local daemon supervised by Prism.' : `Started ${formatTimestamp(current.startedAt)}`
            }
            tone={daemonTone(current)}
            action={
              <Button tone="ghost" size="sm" onClick={() => navigateTo('daemon')}>
                Open Daemon
              </Button>
            }
          >
            <div className="stats">
              <AsyncBoundary<AccountsView>
                state={accounts.state}
                loadingLabel="Counting accounts…"
                empty={<Stat label="Accounts" value="0" />}
                onRetry={accounts.refresh}
              >
                {(list) => (
                  <Stat
                    label="Accounts"
                    value={String(list.accounts.length)}
                    hint={`${list.accounts.filter((account) => account.state === 'active').length} active`}
                  />
                )}
              </AsyncBoundary>
              <AsyncBoundary<ProvidersView>
                state={providers.state}
                loadingLabel="Counting providers…"
                empty={<Stat label="Providers" value="0" />}
                onRetry={providers.refresh}
              >
                {(list) => (
                  <Stat
                    label="Providers"
                    value={String(list.providers.length)}
                    hint={`${list.providers.filter((provider) => provider.enabled !== false).length} enabled`}
                  />
                )}
              </AsyncBoundary>
              <AsyncBoundary<CombosView>
                state={combos.state}
                loadingLabel="Counting combos…"
                empty={<Stat label="Combos" value="0" />}
                onRetry={combos.refresh}
              >
                {(list) => <Stat label="Combos" value={String(list.combos.length)} />}
              </AsyncBoundary>
              <AsyncBoundary<RoutesView>
                state={routes.state}
                loadingLabel="Counting routes…"
                empty={<Stat label="Routes" value="0" />}
                onRetry={routes.refresh}
              >
                {(list) => <Stat label="Routes" value={String(Object.keys(list.routes).length)} />}
              </AsyncBoundary>
              <AsyncBoundary<UsageView>
                state={usage.state}
                loadingLabel="Reading usage…"
                empty={<Stat label="Usage rows" value="0" />}
                onRetry={usage.refresh}
              >
                {(list) => <Stat label="Usage rows" value={String(list.accounts.length)} />}
              </AsyncBoundary>
              <AsyncBoundary<readonly IntegrationStatus[]>
                state={integrations.state}
                loadingLabel="Reading integrations…"
                empty={<Stat label="Managed" value="0" />}
                onRetry={integrations.refresh}
              >
                {(list) => (
                  <Stat
                    label="Managed"
                    value={String(list.filter((entry) => entry.managed).length)}
                    hint={`${list.length} integrations`}
                  />
                )}
              </AsyncBoundary>
            </div>
          </Card>
        )}
      </AsyncBoundary>
      <div className="grid-2">
        <Card
          title="Accounts need attention"
          description="Paused, cooling down, or waiting for reauth."
          action={
            <Button tone="ghost" size="sm" onClick={() => navigateTo('accounts')}>
              Open Accounts
            </Button>
          }
        >
          <AsyncBoundary<AccountsView>
            state={accounts.state}
            loadingLabel="Loading accounts…"
            empty={<Empty title="No accounts configured." />}
            onRetry={accounts.refresh}
          >
            {(list) => {
              const flagged = list.accounts.filter((account) => account.state !== 'active')
              if (flagged.length === 0) return <Empty title="All accounts active." />
              return (
                <ul className="bare-list">
                  {flagged.slice(0, 6).map((account) => (
                    <li key={account.id}>
                      <span className="bare-list__primary">{account.id}</span>
                      <span className={`badge badge--${badgeTone(account.state)}`}>{account.state}</span>
                    </li>
                  ))}
                  {flagged.length > 6 ? (
                    <li>
                      <span className="meta">+{flagged.length - 6} more</span>
                    </li>
                  ) : null}
                </ul>
              )
            }}
          </AsyncBoundary>
        </Card>
        <Card
          title="Next steps"
          description="The three moves that unblock most setups."
          action={
            <Button tone="ghost" size="sm" onClick={() => navigateTo('auth')}>
              Open Auth
            </Button>
          }
        >
          <ol className="steps">
            <li>
              <button type="button" className="steps__link" onClick={() => navigateTo('auth')}>
                Sign in a provider
              </button>
              <span>Codex or Antigravity OAuth lands here via loopback.</span>
            </li>
            <li>
              <button type="button" className="steps__link" onClick={() => navigateTo('combos')}>
                Wire a combo and route
              </button>
              <span>Point an inbound alias at a failover target list.</span>
            </li>
            <li>
              <button type="button" className="steps__link" onClick={() => navigateTo('integrations')}>
                Apply to a client
              </button>
              <span>Write the managed block into Codex, Grok, or OMP.</span>
            </li>
          </ol>
        </Card>
      </div>
    </Stack>
  )
}
