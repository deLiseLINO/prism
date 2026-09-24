import { useState } from 'react'
import type { CSSProperties } from 'react'
import type {
  AgentStatus,
  AgentsView,
  IntegrationApplyResult,
  IntegrationId,
  IntegrationStatus,
  IntegrationToggleResponse,
  IntegrationsView as IntegrationsViewData,
} from '@prism/contracts'
import { AsyncBoundary, Button, Confirm, Empty, Toggle } from '../components/Ui'
import { InstallCell } from '../components/InstallCell'
import { useActiveHost } from '../ActiveHost'
import { useExperimentalFlags } from '../experimental'
import { bridge } from '../bridge'
import { api, ApiError } from '../api'
import { useAsync, useTask, describeError } from '../useAsync'
import type { UseTaskResult } from '../useAsync'



export type RowState = 'managed' | 'unmanaged' | 'uninstalled' | 'drift' | 'damaged'

export function rowState(status: IntegrationStatus): RowState {
  if (!status.installed) return 'uninstalled'
  if (status.detail.includes('fence') && status.detail.includes('damag')) return 'damaged'
  if (status.drift) return 'drift'
  return status.managed ? 'managed' : 'unmanaged'
}

// Bulk targets mirror the per-card buttons except already-converged cards are
// excluded, so each bulk button disables itself when there is nothing to do.
export function applyTargets(list: readonly IntegrationStatus[]): readonly IntegrationStatus[] {
  return list.filter((status) => {
    const state = rowState(status)
    return status.installed && state !== 'damaged' && state !== 'managed'
  })
}

export function rollbackTargets(list: readonly IntegrationStatus[]): readonly IntegrationStatus[] {
  return list.filter((status) => status.installed && status.managed && rowState(status) !== 'damaged')
}

export function visibleIntegrations(list: readonly IntegrationStatus[], showOtherAgents: boolean): readonly IntegrationStatus[] {
  return showOtherAgents ? list : list.filter((status) => status.id === 'omp' || status.id === 'opencode')
}

const stateDot: Record<RowState, string> = {
  managed: 'dot-ok',
  unmanaged: 'dot-muted',
  uninstalled: 'dot-muted',
  drift: 'dot-warn',
  damaged: 'dot-danger',
}

// One attempt, then a single refresh-and-retry when the generation token went
// stale. A retry that lands heals silently; a failure on either attempt
// propagates so the card's refusal banner shows it.
export async function setIntegrationEnabled(
  id: IntegrationId,
  next: boolean,
  expectedGeneration: number,
  notify: () => void,
): Promise<IntegrationToggleResponse> {
  try {
    return await api.integrationToggle(id, { enabled: next, expectedGeneration })
  } catch (err) {
    if (!(err instanceof ApiError) || err.code !== 'stale_generation') throw err
  }
  notify()
  const fresh = await api.integrationsStatus()
  return api.integrationToggle(id, { enabled: next, expectedGeneration: fresh.generation })
}

interface IntegrationCardProps {
  readonly status: IntegrationStatus
  readonly agent: AgentStatus | undefined
  readonly index: number
  readonly bulkBusy: boolean
  readonly generation: number
  readonly remote: boolean
  readonly onChanged: () => void
}
function IntegrationCard({
  status,
  agent,
  index,
  bulkBusy,
  generation,
  remote,
  onChanged,
}: IntegrationCardProps): JSX.Element {

  const applyTask = useTask()
  const rollbackTask = useTask()
  const toggleTask = useTask()
  const busy = applyTask.running || rollbackTask.running || toggleTask.running || bulkBusy
  const taskError = applyTask.error ?? rollbackTask.error ?? toggleTask.error
  const [actionError, setActionError] = useState<string | null>(null)
  const [pendingTakeover, setPendingTakeover] = useState<IntegrationApplyResult & { readonly ok: false } | null>(null)
  const state = rowState(status)
  const damaged = state === 'damaged'
  const cardTone = damaged ? ' int-card--down' : state === 'drift' ? ' int-card--alert' : ''

  async function toggle(id: IntegrationId, next: boolean, expectedGeneration: number): Promise<void> {
    const result = await toggleTask.run(() =>
      setIntegrationEnabled(id, next, expectedGeneration, onChanged),
    )
    if (result === undefined) return
    onChanged()
  }

  async function apply(id: IntegrationId, force: boolean): Promise<void> {
    const result = await applyTask.run(() => api.integrationApply(id, force))

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
    const result = await rollbackTask.run(() => api.integrationRollback(id))
    if (result === undefined) return
    if (!result.ok) {
      setActionError(result.reason === '' ? 'reason not reported' : result.reason)
      return
    }
    setActionError(null)
    onChanged()
  }

  return (
    <div className={`int-row-wrap panel card int-card${cardTone}`} style={{ '--i': index + 1 } as CSSProperties}>
      <div className="int-card-top">
        <span className="int-name">{status.id}</span>
        <span className={`int-status int-status--${state}`}>
          <span className={`dot ${stateDot[state]}`} aria-hidden="true" />
          {state}
        </span>
      </div>
      {status.targetPath !== null && status.targetPath !== '' ? (
        <span className="int-path" title={status.targetPath}>{status.targetPath}</span>
      ) : (
        <span className="int-path int-path--empty">no config path</span>
      )}
      {agent !== undefined ? (
        <InstallCell status={agent} onChanged={onChanged} />
      ) : (
        <span className="int-path int-path--empty">binary status unavailable</span>
      )}
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
      <div className="int-card-foot">
        <span className="int-src">{damaged ? 'apply blocked until the fence is repaired' : status.endpoint ?? 'no endpoint reported'}</span>
        <Toggle
          checked={status.enabled}
          onChange={(next) => void toggle(status.id, next, generation)}
          label="Auto-apply"
          disabled={!status.installed || remote || busy}
        />
        <span className="int-actions">
          <Button
            tone={status.managed ? 'ghost' : 'primary'}
            size="sm"
            onClick={() => void apply(status.id, false)}
            busy={applyTask.running}
            disabled={!status.installed || damaged || busy}
          >
            Apply
          </Button>
          <Button
            tone="ghost"
            size="sm"
            onClick={() => void rollback(status.id)}
            busy={rollbackTask.running}
            disabled={!status.installed || !status.managed || damaged || busy}
          >
            Rollback
          </Button>
        </span>
      </div>
    </div>
  )
}

interface IntegrationsBoardProps {
  readonly list: readonly IntegrationStatus[]
  readonly byAgent: ReadonlyMap<string, AgentStatus>
  readonly generation: number
  readonly remote: boolean
  readonly onChanged: () => void
}

function IntegrationsBoard({ list, byAgent, generation, remote, onChanged }: IntegrationsBoardProps): JSX.Element {
  const applyAllTask = useTask()
  const rollbackAllTask = useTask()
  const bulkBusy = applyAllTask.running || rollbackAllTask.running
  const bulkTaskError = applyAllTask.error ?? rollbackAllTask.error
  const [bulkError, setBulkError] = useState<string | null>(null)
  const applies = applyTargets(list)
  const rollbacks = rollbackTargets(list)
  const installed = list.filter((s) => s.installed).length
  const managed = list.filter((s) => s.managed).length
  const drift = list.filter((s) => s.installed && s.drift).length
  const damaged = list.filter((s) => rowState(s) === 'damaged').length

  async function runBulk(
    task: UseTaskResult,
    targets: readonly IntegrationStatus[],
    mode: 'apply' | 'rollback',
  ): Promise<void> {
    const failures: string[] = []
    const refused = await task.run(async () => {
      for (const target of targets) {
        const result = mode === 'apply'
          ? await api.integrationApply(target.id, false)
          : await api.integrationRollback(target.id)
        if (!result.ok) {
          failures.push(`${target.id}: ${result.reason === '' ? 'reason not reported' : result.reason}`)
        }
        onChanged()
      }
      return failures.length
    })
    if (refused === undefined) return
    setBulkError(refused === 0 ? null : `${mode} all: ${refused} of ${targets.length} refused (${failures.join('; ')})`)
  }

  return (
    <div className="int-screen" style={{ '--i': 1 } as CSSProperties}>
      <div className="int-strip">
        <div className="int-stats">
          <span className="int-stat"><span className="dot dot-ok" aria-hidden="true" /><span className="int-stat__n">{installed}</span>installed</span>
          <span className="int-stat"><span className="dot dot-ok" aria-hidden="true" /><span className="int-stat__n">{managed}</span>managed</span>
          <span className="int-stat"><span className="dot dot-warn" aria-hidden="true" /><span className="int-stat__n">{drift}</span>drift</span>
          <span className="int-stat"><span className="dot dot-danger" aria-hidden="true" /><span className="int-stat__n">{damaged}</span>damaged</span>
        </div>
        <span className="int-strip__actions">
          <Button
            tone="primary"
            size="sm"
            busy={applyAllTask.running}
            disabled={bulkBusy || applies.length === 0}
            onClick={() => void runBulk(applyAllTask, applies, 'apply')}
          >
            Apply all
          </Button>
          <Button
            tone="ghost"
            size="sm"
            busy={rollbackAllTask.running}
            disabled={bulkBusy || rollbacks.length === 0}
            onClick={() => void runBulk(rollbackAllTask, rollbacks, 'rollback')}
          >
            Rollback all
          </Button>
        </span>
      </div>
      {bulkTaskError !== null ? (
        <div className="int-refusal" role="alert">
          <span className="int-refusal__msg">{describeError(bulkTaskError)}</span>
        </div>
      ) : bulkError !== null ? (
        <div className="int-refusal" role="alert">
          <span className="int-refusal__msg">{bulkError}</span>
          <button
            type="button"
            className="int-refusal__close"
            aria-label="Dismiss"
            onClick={() => setBulkError(null)}
          >
            ×
          </button>
        </div>
      ) : null}
      <div className="int-grid">
        {list.map((status, index) => (
          <IntegrationCard
            key={status.id}
            status={status}
            agent={byAgent.get(status.id)}
            index={index}
            bulkBusy={bulkBusy}
            generation={generation}
            remote={remote}
            onChanged={onChanged}
          />
        ))}
      </div>
    </div>
  )
}

export function IntegrationsView(): JSX.Element {
  const { host: selected } = useActiveHost()
  const { flags } = useExperimentalFlags()
  const statuses = useAsync<IntegrationsViewData>(() => api.integrationsStatus(), [selected])
  const agents = useAsync<AgentsView>(
    () => selected === 'local' ? bridge.agents.status() : Promise.resolve({ agents: [], actionsEnabled: false }),
    [selected],
  )
  const agentList = agents.state.kind === 'ready' ? agents.state.value.agents : []
  const byAgent = new Map(agentList.map((a) => [a.id, a]))

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
      </div>

      <AsyncBoundary<IntegrationsViewData>
        state={statuses.state}
        loadingLabel={`Loading integrations on ${selected}…`}
        empty={<Empty title="No integrations reported." />}
        onRetry={() => statuses.refresh()}
      >
        {(all) => (
          <IntegrationsBoard
            list={visibleIntegrations(all.integrations, flags.otherAgents)}
            byAgent={byAgent}
            generation={all.generation}
            remote={selected !== 'local'}
            onChanged={() => {
              statuses.refresh()
              agents.refresh()
            }}
          />
        )}
      </AsyncBoundary>
    </section>
  )
}
