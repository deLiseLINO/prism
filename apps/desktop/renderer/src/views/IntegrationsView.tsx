import { useState } from 'react'
import type {
  AgentStatus,
  AgentsView,
  IntegrationApplyResult,
  IntegrationId,
  IntegrationStatus,
  IntegrationToggleResponse,
  IntegrationsView as IntegrationsViewData,
} from '@prism/contracts'
import { AsyncBoundary, Button, Confirm, DetailsList, Empty, Toggle } from '../components/Ui'
import { PageHeader, StatusChip, Toolbar, ToolbarSpacer } from '../components/Patterns'
import { InstallCell } from '../components/InstallCell'
import { useActiveHost } from '../ActiveHost'
import { useExperimentalFlags } from '../experimental'
import { bridge } from '../bridge'
import { api, ApiError } from '../api'
import { useAsync, useTask, describeError } from '../useAsync'
import type { UseTaskResult } from '../useAsync'



export type RowState = 'managed' | 'unmanaged' | 'uninstalled' | 'drift' | 'blocked' | 'damaged' | 'conflict'

export function rowState(status: IntegrationStatus): RowState {
  if (status.blocked === true) return 'blocked'
  if (status.detail.includes('fence') && status.detail.includes('damag')) return 'damaged'
  if (!status.installed) return 'uninstalled'
  if (status.conflict === 'journal') return 'conflict'
  if (status.drift) return 'drift'
  return status.managed ? 'managed' : 'unmanaged'
}

type JournalAction = 'apply' | 'rollback'

export function baseName(path: string): string {
  return path.slice(Math.max(path.lastIndexOf('/'), path.lastIndexOf('\\')) + 1)
}

function journalDialogCopy(action: JournalAction, id: IntegrationId, fileName: string): {
  readonly title: string
  readonly detail: string
  readonly note: string
  readonly confirmLabel: string
} {
  const fields = id === 'claude' ? 'Prism environment fields' : id === 'codex' ? 'Prism routing fields and managed fence' : 'Prism provider block'
  if (action === 'apply') {
    return {
      title: `Reconnect ${fileName || id}?`,
      detail: `Prism cannot use its saved recovery metadata safely. This can follow config edits, damaged metadata, or an unsupported journal. Confirmed reconnect saves the current config, known cache or catalog files, and recovery metadata before updating the ${fields}. The current config becomes the new rollback baseline.`,
      note: 'Old originals remain in the saved backups but are not automatically restorable. New Rollback restores the pre-reconnect state, not those originals. Keep a separate copy of any config you want to preserve.',
      confirmLabel: 'Reconnect',
    }
  }
  return {
    title: `Remove Prism from ${fileName || id}?`,
    detail: `Prism saves the current config and recovery metadata, then removes only the ${fields}. Unrelated config is kept. Earlier originals are not restored.`,
    note: 'Unknown side files may remain. Keep a separate copy of any config you want to preserve. Saved backup paths will be shown after the operation, including if it fails.',
    confirmLabel: id === 'claude' ? 'Remove Prism fields' : id === 'codex' ? 'Remove Prism routing' : 'Remove Prism block',
  }
}

function savedPaths(result: IntegrationApplyResult): readonly string[] {
  return [...new Set([...(result.backups ?? []), ...(result.backup === undefined ? [] : [result.backup])])]
}

// Bulk targets mirror the per-card buttons except already-converged cards are
// excluded, so each bulk button disables itself when there is nothing to do.
export function applyTargets(list: readonly IntegrationStatus[]): readonly IntegrationStatus[] {
  return list.filter((status) => {
    const state = rowState(status)
    return status.installed && state !== 'blocked' && state !== 'damaged' && state !== 'conflict' && state !== 'managed'
  })
}

export function rollbackTargets(list: readonly IntegrationStatus[]): readonly IntegrationStatus[] {
  return list.filter((status) => status.installed && status.managed && rowState(status) !== 'blocked' && rowState(status) !== 'damaged' && rowState(status) !== 'conflict')
}

export function visibleIntegrations(list: readonly IntegrationStatus[], showOtherAgents: boolean): readonly IntegrationStatus[] {
  return showOtherAgents ? list : list.filter((status) => status.id === 'omp' || status.id === 'opencode')
}

const stateTone: Record<RowState, 'ok' | 'warn' | 'danger' | 'muted'> = {
  managed: 'ok',
  unmanaged: 'muted',
  uninstalled: 'muted',
  drift: 'warn',
  blocked: 'danger',
  damaged: 'danger',
  conflict: 'warn',
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
  readonly bulkBusy: boolean
  readonly generation: number
  readonly remote: boolean
  readonly onChanged: () => void
}
function IntegrationCard({
  status,
  agent,
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
  const [journalDialog, setJournalDialog] = useState<JournalAction | null>(null)
  const [receipt, setReceipt] = useState<{ readonly kind: JournalAction; readonly result: IntegrationApplyResult } | null>(null)
  const state = rowState(status)
  const blocked = state === 'blocked' || state === 'damaged'
  const needsRepair = blocked || state === 'conflict'
  const configPath = status.targetPath ?? ''
  const fileName = baseName(configPath)
  const dialogCopy = journalDialog === null ? null : journalDialogCopy(journalDialog, status.id, fileName)
  const backups = receipt === null ? [] : savedPaths(receipt.result)

  function review(action: JournalAction): void {
    if (busy || blocked) return
    setPendingTakeover(null)
    setJournalDialog(action)
  }

  async function toggle(id: IntegrationId, next: boolean, expectedGeneration: number): Promise<void> {
    const result = await toggleTask.run(() =>
      setIntegrationEnabled(id, next, expectedGeneration, onChanged),
    )
    if (result === undefined) return
    onChanged()
  }

  async function apply(id: IntegrationId, force: boolean): Promise<void> {
    if (busy || blocked) return
    if (state === 'conflict' && !force) {
      review('apply')
      return
    }
    const result = await applyTask.run(() => api.integrationApply(id, force))
    if (result === undefined) return
    setReceipt({ kind: 'apply', result })
    onChanged()
    if (!result.ok) {
      if (result.conflict === 'journal' && !force) {
        setJournalDialog('apply')
        setPendingTakeover(null)
        setActionError(null)
        return
      }
      if (result.retryable === true && !force && result.conflict === undefined) {
        setPendingTakeover(result)
        setActionError(null)
        return
      }
      setPendingTakeover(null)
      setJournalDialog(null)
      setActionError(result.reason === '' ? 'reason not reported' : result.reason)
      return
    }
    setPendingTakeover(null)
    setJournalDialog(null)
    setActionError(null)
  }

  async function rollback(id: IntegrationId, force: boolean): Promise<void> {
    if (busy || blocked) return
    if (state === 'conflict' && !force) {
      review('rollback')
      return
    }
    const result = await rollbackTask.run(() => api.integrationRollback(id, force))
    if (result === undefined) return
    setReceipt({ kind: 'rollback', result })
    onChanged()
    if (!result.ok) {
      if (result.conflict === 'journal' && !force) {
        setJournalDialog('rollback')
        setActionError(null)
        return
      }
      setJournalDialog(null)
      setActionError(result.reason === '' ? 'reason not reported' : result.reason)
      return
    }
    setJournalDialog(null)
    setActionError(null)
  }

  return (
    <div className="int-item">
      <div className="lrow lrow--nolead int-row">
        <div className="lrow__main">
          <div className="lrow__title">
            <span>{status.id}</span>
            <StatusChip tone={stateTone[state]}>{state === 'conflict' ? 'Needs repair' : state === 'blocked' ? 'Blocked' : state}</StatusChip>
          </div>
          <div className="lrow__sub-line">
            {status.targetPath !== null && status.targetPath !== '' ? (
              <span className="int-path" title={status.targetPath}>{status.targetPath}</span>
            ) : (
              <span className="int-path int-path--empty">no config path</span>
            )}
            <span className="int-src">{needsRepair ? status.enabled ? 'auto-apply paused until repaired' : 'auto-apply off; repair needed' : status.endpoint ?? 'no endpoint reported'}</span>
          </div>
          {agent !== undefined ? (
            <InstallCell status={agent} onChanged={onChanged} />
          ) : (
            <span className="int-path int-path--empty">binary status unavailable</span>
          )}
        </div>
        <div className="lrow__side">
          <Toggle
            checked={status.enabled}
            onChange={(next) => void toggle(status.id, next, generation)}
            label="Auto-apply"
            disabled={!status.installed || remote || busy}
          />
          <Button
            tone={status.managed ? 'ghost' : 'primary'}
            onClick={() => void apply(status.id, false)}
            busy={applyTask.running}
            disabled={!status.installed || blocked || busy}
          >
            Apply
          </Button>
          <Button
            tone="ghost"
            onClick={() => void rollback(status.id, false)}
            busy={rollbackTask.running}
            disabled={!status.installed || (!status.managed && state !== 'conflict') || blocked || busy}
          >
            Rollback
          </Button>
        </div>
      </div>
      {needsRepair ? (
        <div
          className={`lrow-note ${blocked ? '' : 'lrow-note--warn lrow-note--click'}`}
          role="status"
          onClick={() => review('apply')}
        >
          <div className="lrow-note__body">
            <p className="lrow-note__title">{blocked ? 'Config blocked' : 'Needs repair'}</p>
            <p>{status.detail || 'Problem details were not reported.'}</p>
            <p>{status.enabled ? 'Auto-apply is paused until this is repaired.' : 'Auto-apply is off. This still needs repair before applying.'}</p>
            {blocked ? <p>Repair the config or file access first. Confirmation cannot bypass this problem.</p> : null}
          </div>
          {!blocked ? (
            <Button tone="primary" size="sm" disabled={busy} onClick={() => review('apply')}>
              Review
            </Button>
          ) : null}
        </div>
      ) : null}
      {taskError !== null ? (
        <div className="lrow-note" role="alert">
          <span className="lrow-note__msg">{describeError(taskError)}</span>
        </div>
      ) : null}
      {taskError === null && actionError !== null ? (
        <div className="lrow-note" role="alert">
          <span className="lrow-note__msg">{actionError}</span>
          <button
            type="button"
            className="lrow-note__close"
            aria-label="Dismiss"
            onClick={() => setActionError(null)}
          >
            ×
          </button>
        </div>
      ) : null}
      {!blocked && taskError === null && pendingTakeover !== null ? (
        <div className="lrow-confirm">
          <Confirm
            title={`Take over ${pendingTakeover.id} config?`}
            detail={`${pendingTakeover.reason} Confirmed apply displaces your own values, journals them, and rollback restores them verbatim.`}
            confirmLabel="Take over"
            busy={busy}
            onCancel={() => setPendingTakeover(null)}
            onConfirm={() => void apply(pendingTakeover.id, true)}
          />
        </div>
      ) : null}
      {!blocked && journalDialog !== null && dialogCopy !== null ? (
        <div className="lrow-confirm">
          <Confirm
            tone="warn"
            title={dialogCopy.title}
            detail={dialogCopy.detail}
            note={dialogCopy.note}
            details={configPath === '' ? undefined : [{ label: 'Config', value: configPath }]}
            confirmLabel={dialogCopy.confirmLabel}
            busy={busy}
            onCancel={() => setJournalDialog(null)}
            onConfirm={() => void (journalDialog === 'apply' ? apply(status.id, true) : rollback(status.id, true))}
          />
        </div>
      ) : null}
      {receipt !== null && backups.length > 0 ? (
        <div className={`lrow-note ${receipt.result.ok ? 'lrow-note--ok' : 'lrow-note--warn'}`} role="status">
          <div className="lrow-note__body">
            <p className="lrow-note__title">{receipt.result.ok ? receipt.kind === 'apply' ? 'Applied; backups saved' : 'Prism configuration removed; backups saved' : 'Operation failed; backups saved'}</p>
            <p>These paths were saved by the operation. Keep them if you need to recover earlier config or metadata.</p>
            <DetailsList rows={backups.map((path, index) => ({ label: `Backup ${index + 1}`, value: path }))} />
          </div>
          <button
            type="button"
            className="lrow-note__close"
            aria-label="Dismiss backups"
            onClick={() => setReceipt(null)}
          >
            ×
          </button>
        </div>
      ) : null}
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
  const [bulkBackups, setBulkBackups] = useState<readonly { readonly label: string; readonly value: string }[]>([])
  const applies = applyTargets(list)
  const rollbacks = rollbackTargets(list)
  const installed = list.filter((s) => s.installed).length
  const managed = list.filter((s) => rowState(s) === 'managed').length
  const drift = list.filter((s) => rowState(s) === 'drift').length
  const damaged = list.filter((s) => rowState(s) === 'damaged').length
  const blocked = list.filter((s) => rowState(s) === 'blocked').length
  const conflicts = list.filter((s) => rowState(s) === 'conflict').length

  async function runBulk(
    task: UseTaskResult,
    targets: readonly IntegrationStatus[],
    mode: 'apply' | 'rollback',
  ): Promise<void> {
    const failures: string[] = []
    setBulkBackups([])
    const refused = await task.run(async () => {
      for (const target of targets) {
        const result = mode === 'apply'
          ? await api.integrationApply(target.id, false)
          : await api.integrationRollback(target.id, false)
        const paths = savedPaths(result)
        if (paths.length > 0) {
          setBulkBackups((previous) => [...previous, ...paths.map((path, index) => ({ label: `${target.id} backup ${index + 1}`, value: path }))])
        }
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
    <>
      <Toolbar label="Integration actions">
        <div className="int-stats" role="status">
          <span className="chip"><span className="int-stat__n">{installed}</span>installed</span>
          <span className="chip"><span className="int-stat__n">{managed}</span>managed</span>
          <span className="chip"><span className="int-stat__n">{drift}</span>drift</span>
          <span className="chip"><span className="int-stat__n">{damaged}</span>damaged</span>
          <span className="chip"><span className="int-stat__n">{blocked}</span>blocked</span>
          <span className="chip"><span className="int-stat__n">{conflicts}</span>needs repair</span>
        </div>
        <ToolbarSpacer />
        <Button
          tone="ghost"
          busy={rollbackAllTask.running}
          disabled={bulkBusy || rollbacks.length === 0}
          onClick={() => void runBulk(rollbackAllTask, rollbacks, 'rollback')}
        >
          Rollback all
        </Button>
        <Button
          tone="primary"
          busy={applyAllTask.running}
          disabled={bulkBusy || applies.length === 0}
          onClick={() => void runBulk(applyAllTask, applies, 'apply')}
        >
          Apply all
        </Button>
      </Toolbar>
      {bulkTaskError !== null ? (
        <div className="lrow-note" role="alert">
          <span className="lrow-note__msg">{describeError(bulkTaskError)}</span>
        </div>
      ) : bulkError !== null ? (
        <div className="lrow-note" role="alert">
          <span className="lrow-note__msg">{bulkError}</span>
          <button
            type="button"
            className="lrow-note__close"
            aria-label="Dismiss"
            onClick={() => setBulkError(null)}
          >
            ×
          </button>
        </div>
      ) : null}
      {bulkBackups.length > 0 ? (
        <div className="lrow-note" role="status">
          <div className="lrow-note__body">
            <p className="lrow-note__title">Bulk operation backups</p>
            <DetailsList rows={bulkBackups} />
          </div>
        </div>
      ) : null}
      <div className="list">
        {list.map((status) => (
          <IntegrationCard
            key={status.id}
            status={status}
            agent={byAgent.get(status.id)}
            bulkBusy={bulkBusy}
            generation={generation}
            remote={remote}
            onChanged={onChanged}
          />
        ))}
      </div>
    </>
  )
}

export function IntegrationsView(): JSX.Element {
  const { host: selected } = useActiveHost()
  const { flags } = useExperimentalFlags()
  const statuses = useAsync<IntegrationsViewData>(() => api.integrationsStatus(), [selected])
  const agents = useAsync<AgentsView>(
    () => selected === 'local' ? bridge.agents.status() : Promise.resolve({ agents: [] }),
    [selected],
  )
  const agentList = agents.state.kind === 'ready' ? agents.state.value.agents : []
  const byAgent = new Map(agentList.map((a) => [a.id, a]))

  return (
    <section className="screen" aria-labelledby="h-integrations">
      <PageHeader id="h-integrations" icon="#i-puzzle" title="Integrations" />
      <AsyncBoundary<IntegrationsViewData>
        state={statuses.state}
        loadingLabel={`Loading integrations on ${selected}…`}
        empty={<Empty icon="#i-puzzle" title="No integrations reported." />}
        onRetry={() => statuses.refresh()}
      >
        {(all) => (
          <IntegrationsBoard
            key={selected}
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
