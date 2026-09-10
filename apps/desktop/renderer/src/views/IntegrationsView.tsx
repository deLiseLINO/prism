import { useState } from 'react'
import type { CSSProperties } from 'react'
import type { AgentStatus, HostView, IntegrationApplyResult, IntegrationId, IntegrationStatus } from '@prism/contracts'
import { AsyncBoundary, Button, Confirm, Empty, Select } from '../components/Ui'
import { InstallCell } from '../components/InstallCell'

import { useAsync, useTask, describeError } from '../useAsync'

type RowState = 'managed' | 'unmanaged' | 'uninstalled' | 'drift' | 'damaged'

function rowState(status: IntegrationStatus): RowState {
  if (!status.installed) return 'uninstalled'
  if (status.detail.includes('fence') && status.detail.includes('damag')) return 'damaged'
  if (status.drift) return 'drift'
  return status.managed ? 'managed' : 'unmanaged'
}

const stateDot: Record<RowState, string> = {
  managed: 'dot-ok',
  unmanaged: 'dot-muted',
  uninstalled: 'dot-muted',
  drift: 'dot-warn',
  damaged: 'dot-danger',
}
interface IntegrationRowProps {
  readonly status: IntegrationStatus
  readonly agent: AgentStatus | undefined
  readonly onChanged: () => void
  readonly host: string
}
function IntegrationRow({ status, agent, onChanged, host }: IntegrationRowProps): JSX.Element {

  const applyTask = useTask()
  const rollbackTask = useTask()
  const busy = applyTask.running || rollbackTask.running
  const taskError = applyTask.error ?? rollbackTask.error
  const [actionError, setActionError] = useState<string | null>(null)
  const [pendingTakeover, setPendingTakeover] = useState<IntegrationApplyResult & { readonly ok: false } | null>(null)
  const state = rowState(status)
  const damaged = state === 'damaged'

  async function apply(id: IntegrationId, force: boolean): Promise<void> {
    const result = await applyTask.run(() => window.prism.integrations.apply({ id, ...(force ? { force: true as const } : {}), ...(host !== 'local' ? { host } : {}) }))
    if (result === undefined) return
    if (!result.ok) {
      if (result.retryable === true && !force) {
        setPendingTakeover(result)
        setActionError(null)
        return
      }
      setPendingTakeover(null)
      setActionError(result.reason === '' ? 'reason not reported' : result.reason)
      return
    }
    setPendingTakeover(null)
    setActionError(null)
    onChanged()
  }

  async function rollback(id: IntegrationId): Promise<void> {
    const result = await rollbackTask.run(() => window.prism.integrations.rollback({ id, ...(host !== 'local' ? { host } : {}) }))
    if (result === undefined) return
    if (!result.ok) {
      setActionError(result.reason === '' ? 'reason not reported' : result.reason)
      return
    }
    setActionError(null)
    onChanged()
  }

  return (
    <div className="int-row-wrap">
      <div className="int-row">
        <div className="int-cell int-name">{status.id}</div>
        <div className={`int-cell int-status int-status--${state}`}>
          <span className={`dot ${stateDot[state]}`} aria-hidden="true" />
          {state}
        </div>
        <div className="int-cell int-pathbox">
          {status.targetPath !== null && status.targetPath !== '' ? (
            <span className="int-path" title={status.targetPath}>{status.targetPath}</span>
          ) : (
            <span className="int-path int-path--empty">no config path</span>
          )}
        </div>
        <div className="int-cell int-installbox">
          {agent !== undefined ? (
            <InstallCell status={agent} onChanged={onChanged} />
          ) : (
            <span className="int-path int-path--empty">binary status unavailable</span>
          )}
        </div>
        <div className="int-cell int-actions">
          <Button
            tone={status.managed ? 'ghost' : 'primary'}
            size="sm"
            onClick={() => void apply(status.id, false)}
            disabled={!status.installed || damaged || busy}
          >
            Apply
          </Button>
          <Button
            tone="ghost"
            size="sm"
            onClick={() => void rollback(status.id)}
            disabled={!status.installed || !status.managed || damaged || busy}
          >
            Rollback
          </Button>
        </div>
      </div>
      {taskError !== null ? (
        <div className="int-refusal" role="alert">
          <span className="int-refusal__msg">{describeError(taskError)}</span>
        </div>
      ) : null}
      {taskError === null && actionError !== null ? (
        <div className="int-refusal" role="alert">
          <span className="int-refusal__msg">{actionError}</span>
          <button
            type="button"
            className="int-refusal__close"
            aria-label="Dismiss"
            onClick={() => setActionError(null)}
          >
            ×
          </button>
        </div>
      ) : null}
      {taskError === null && pendingTakeover !== null ? (
        <Confirm
          title={`Take over ${pendingTakeover.id} config?`}
          detail={`${pendingTakeover.reason} Confirmed apply displaces your own values, journals them, and rollback restores them verbatim.`}
          confirmLabel="Take over"
          busy={busy}
          onCancel={() => setPendingTakeover(null)}
          onConfirm={() => void apply(pendingTakeover.id, true)}
        />
      ) : null}
    </div>
  )
}

function StatsStrip({ list }: { readonly list: readonly IntegrationStatus[] }): JSX.Element {
  const installed = list.filter((s) => s.installed).length
  const managed = list.filter((s) => s.managed).length
  const drift = list.filter((s) => s.installed && s.drift).length
  const damaged = list.filter((s) => rowState(s) === 'damaged').length
  return (
    <div className="int-strip">
      <div className="int-stats">
        <span className="int-stat"><span className="dot dot-ok" aria-hidden="true" /><span className="int-stat__n">{installed}</span>installed</span>
        <span className="int-stat"><span className="dot dot-ok" aria-hidden="true" /><span className="int-stat__n">{managed}</span>managed</span>
        <span className="int-stat"><span className="dot dot-warn" aria-hidden="true" /><span className="int-stat__n">{drift}</span>drift</span>
        <span className="int-stat"><span className="dot dot-danger" aria-hidden="true" /><span className="int-stat__n">{damaged}</span>damaged</span>
      </div>
    </div>
  )
}

function hostLabel(host: HostView): string {
  return host.local ? 'This machine' : host.status === 'ok' ? host.id : `${host.id} (${host.status})`
}

export function IntegrationsView(): JSX.Element {
  const [hostId, setHostId] = useState<string>('local')
  const hosts = useAsync<readonly HostView[]>(async () => {
    const view = await window.prism.integrations.hosts()
    return view.hosts
  }, [])
  const hostOptions = hosts.state.kind === 'ready' && hosts.state.value.length > 0
    ? hosts.state.value
    : ([{ id: 'local', local: true, status: 'ok' }] as readonly HostView[])
  const selected = hostOptions.some((h) => h.id === hostId) ? hostId : (hostOptions[0]?.id ?? 'local')
  const statuses = useAsync<readonly IntegrationStatus[]>(() => window.prism.integrations.status(selected), [selected])
  const agents = useAsync<readonly AgentStatus[]>(() => window.prism.agents.status(), [])
  const byAgent = new Map((agents.state.kind === 'ready' ? agents.state.value : []).map((a) => [a.id, a]))

  return (
    <section className="screen" aria-labelledby="h-integrations">
      <div className="screen-head" style={{ '--i': 0 } as CSSProperties}>
        <div>
          <h1 id="h-integrations">
            <svg width="19" height="19" className="h-ic">
              <use href="#i-puzzle" />
            </svg>
            Integrations
          </h1>
          <p className="sub">config apply and rollback for each CLI Prism manages</p>
        </div>
        <div className="int-host">
          <label className="int-host__label" htmlFor="int-host-select">Host</label>
          <Select<string>
            id="int-host-select"
            value={selected}
            onChange={setHostId}
            options={hostOptions.map((h) => ({ value: h.id, label: hostLabel(h) }))}
          />
        </div>
      </div>
      <AsyncBoundary<readonly IntegrationStatus[]>
        state={statuses.state}
        loadingLabel={`Loading integrations on ${selected}…`}
        empty={<Empty title="No integrations reported." />}
        onRetry={() => statuses.refresh()}
      >
        {(list) => (
          <div className="int-screen" style={{ '--i': 1 } as CSSProperties}>
            <StatsStrip list={list} />
            <div className="int-list">
              {list.map((status) => (
                <IntegrationRow
                  key={status.id}
                  status={status}
                  agent={byAgent.get(status.id)}
                  host={selected}
                  onChanged={() => {
                    statuses.refresh()
                    agents.refresh()
                  }}
                />

              ))}
            </div>
          </div>
        )}
      </AsyncBoundary>
    </section>
  )
}
