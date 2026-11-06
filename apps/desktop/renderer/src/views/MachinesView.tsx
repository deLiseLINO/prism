import { useEffect, useState, type MouseEvent } from 'react'
import type { HostSyncView, HostView } from '@prism/contracts'
import { api } from '../api'
import { useActiveHost } from '../ActiveHost'
import { getInstallRun, onInstallSettled, startInstall, useInstallRun } from '../installRuns'
import { AsyncBoundary, Button, Confirm, Empty, Field, TextInput, Toggle } from '../components/Ui'
import { PageHeader, Section, StatusChip } from '../components/Patterns'
import { describeError, useAsync, useTask } from '../useAsync'

const SYNC_PENDING_POLL_MS = 3_000

function relativeTime(iso: string, now: number): string {
  const at = new Date(iso).getTime()
  if (Number.isNaN(at)) return 'at an unknown time'
  const seconds = Math.max(0, Math.round((now - at) / 1000))
  if (seconds < 60) return 'just now'
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes} min ago`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours} h ago`
  return `${Math.floor(hours / 24)} d ago`
}

export function syncLabel(sync: HostSyncView | undefined, autoSync: boolean, now: number): string {
  if (!autoSync || sync?.state === 'off') return 'Auto sync off'
  if (sync === undefined || sync.state === 'pending') return 'Waiting for first sync'
  if (sync.state === 'failed') return `Sync failed: ${sync.error ?? 'reason not reported'}`
  return sync.at === undefined ? 'Synced' : `Synced ${relativeTime(sync.at, now)}`
}

interface MachineRowProps {
  readonly host: HostView
  readonly generation: number
  readonly onChanged: () => void
  readonly active: boolean
  readonly onManage: (host: string) => void
}

function MachineRow({ host, generation, onChanged, active, onManage }: MachineRowProps): JSX.Element {
  const [pendingDelete, setPendingDelete] = useState(false)
  const task = useTask()
  const syncTask = useTask()
  const installRun = useInstallRun(host.id)
  const installing = installRun.phase !== null
  const busy = task.running || installing

  async function installDaemon(): Promise<void> {
    await startInstall(host.id)
    if (getInstallRun(host.id).done) onChanged()
  }

  async function remove(): Promise<void> {
    const result = await task.run(() => api.localDeleteHost(host.id, generation))
    if (result === undefined) return
    setPendingDelete(false)
    onChanged()
  }

  async function setAutoSync(enabled: boolean): Promise<void> {
    const result = await syncTask.run(() => api.localSetHostAutoSync(host.id, { enabled, expectedGeneration: generation }))
    if (result === undefined) return
    onChanged()
  }

  if (host.local === true) {
    return (
      <div className="lrow lrow--nolead">
        <div className="lrow__main">
          <div className="lrow__title">
            <span>{host.id}</span>
            <StatusChip tone="ok">this machine</StatusChip>
          </div>
          <span className="lrow__sub lrow__sub--mono">config lives here</span>
        </div>
        <div className="lrow__side">
          <Button tone={active ? 'primary' : 'ghost'} onClick={() => onManage('local')}>
            {active ? 'Managing' : 'Manage here'}
          </Button>
        </div>
      </div>
    )
  }

  const installed = host.status === 'ok'
  const autoSync = host.autoSync !== false
  const syncText = syncLabel(host.sync, autoSync, Date.now())
  const syncFailed = autoSync && host.sync?.state === 'failed'
  const toggleRow = (event: MouseEvent<HTMLDivElement>): void => {
    if (syncTask.running || (event.target as HTMLElement).closest('.toggle') !== null) return
    void setAutoSync(!autoSync)
  }
  return (
    <div className="mach-item">
      <div className="lrow lrow--nolead">
        <div className="lrow__main">
          <div className="lrow__title">
            <span>{host.id}</span>
            <StatusChip tone={installed ? 'ok' : 'warn'}>
              {installing ? 'installing' : installed ? 'installed' : 'not installed'}
            </StatusChip>
          </div>
          <span className="lrow__sub lrow__sub--mono lrow__sub--clamp" title={host.address ?? ''}>
            {host.address ?? ''}
          </span>
        </div>
        <div className="lrow__side">
          <Button tone={active ? 'primary' : 'ghost'} onClick={() => onManage(host.id)} disabled={!installed}>
            {active ? 'Managing' : 'Manage'}
          </Button>
          {!installed || installing ? (
            <Button tone="ghost" onClick={() => void installDaemon()} disabled={busy} busy={installing}>
              {installing ? 'Installing…' : installRun.error !== null ? 'Retry install' : 'Install prismd'}
            </Button>
          ) : null}
          <Button tone="ghost" onClick={() => setPendingDelete(true)} disabled={busy}>
            Remove
          </Button>
        </div>
      </div>
      {installed ? (
        <div
          className={`mach-sync${syncTask.running ? ' mach-sync--busy' : ''}`}
          onClick={toggleRow}
          aria-disabled={syncTask.running}
        >
          <Toggle checked={autoSync} onChange={(next) => void setAutoSync(next)} label="Auto sync" disabled={syncTask.running} />
          <span className={`mach-sync__status${syncFailed ? ' mach-sync__status--failed' : ''}`} title={syncText}>
            {syncText}
          </span>
        </div>
      ) : null}
      {syncTask.error !== null ? (
        <div className="lrow-note" role="alert">
          <span className="lrow-note__msg">{describeError(syncTask.error)}</span>
        </div>
      ) : null}
      {task.error !== null ? (
        <div className="lrow-note" role="alert">
          <span className="lrow-note__msg">{describeError(task.error)}</span>
        </div>
      ) : null}
      {task.error === null && installRun.error !== null ? (
        <div className="lrow-note" role="alert">
          <span className="lrow-note__msg">{installRun.error}</span>
        </div>
      ) : null}
      {installRun.notice !== null ? (
        <div className="lrow-note lrow-note--warn" role="status">
          <span className="lrow-note__msg">{installRun.notice}</span>
        </div>
      ) : null}
      {pendingDelete ? (
        <div className="lrow-confirm">
          <Confirm
            title={`Remove machine ${host.id}?`}
            detail="The config entry goes away and settings stop syncing to that machine. Its daemon and the config it already has stay as they are."
            confirmLabel="Remove"
            busy={busy}
            onCancel={() => setPendingDelete(false)}
            onConfirm={() => void remove()}
          />
        </div>
      ) : null}
    </div>
  )
}

export function MachinesView(): JSX.Element {
  const { host: activeHost, setHost, setHosts } = useActiveHost()
  const [id, setId] = useState('')
  const [address, setAddress] = useState('')
  const addTask = useTask()
  const hosts = useAsync<readonly HostView[]>(async () => {
    const view = await api.localHosts()
    return view.hosts
  }, [])
  const generation = useAsync<number>(async () => {
    const providers = await api.localProviders()
    return providers.generation
  }, [])

  useEffect(() => onInstallSettled(() => {
    hosts.refresh()
    generation.refresh()
  }), [hosts.refresh, generation.refresh])

  const list = hosts.state.kind === 'ready' ? hosts.state.value : null

  useEffect(() => {
    if (list !== null) setHosts(list)
  }, [list, setHosts])

  const syncPending = list?.some((host) => host.status === 'ok' && host.autoSync !== false && host.sync?.state === 'pending') === true
  useEffect(() => {
    if (!syncPending) return
    const timer = window.setInterval(() => hosts.refresh(), SYNC_PENDING_POLL_MS)
    return () => window.clearInterval(timer)
  }, [syncPending, hosts.refresh])

  const busy = addTask.running
  const gen = generation.state.kind === 'ready' ? generation.state.value : 0

  async function add(): Promise<void> {
    const result = await addTask.run(() => api.localCreateHost({
      id: id.trim(),
      address: address.trim(),
      expectedGeneration: gen,
    }))
    if (result === undefined) return
    setId('')
    setAddress('')
    hosts.refresh()
    generation.refresh()
    if (result.host.status === 'not_installed') void startInstall(result.host.id)
  }

  const canAdd = id.trim() !== '' && address.trim() !== '' && generation.state.kind === 'ready' && !busy

  return (
    <section className="screen" aria-labelledby="h-machines">
      <PageHeader id="h-machines" icon="#i-cube" title="Machines" />
      <Section title="Add a machine">
        <form
          className="panel mach-add ctl-row ctl-row--end"
          onSubmit={(event) => {
            event.preventDefault()
            if (canAdd) void add()
          }}
        >
          <Field label="Name" htmlFor="machine-id">
            <TextInput
              id="machine-id"
              type="text"
              value={id}
              onChange={setId}
              placeholder="workmac"
              disabled={busy}
            />
          </Field>
          <Field label="SSH address" htmlFor="machine-address">
            <TextInput
              id="machine-address"
              type="text"
              value={address}
              onChange={setAddress}
              placeholder="user@192.168.1.20"
              disabled={busy}
            />
          </Field>
          <Button type="submit" tone="primary" onClick={() => undefined} disabled={!canAdd} busy={busy}>
            Add machine
          </Button>
        </form>
      </Section>
      {addTask.error !== null ? (
        <div className="lrow-note lrow-note--card" role="alert">
          <span className="lrow-note__msg">{describeError(addTask.error)}</span>
        </div>
      ) : null}
      <div>
        <AsyncBoundary<readonly HostView[]>
          state={hosts.state}
          loadingLabel="Loading machines…"
          empty={<Empty icon="#i-cube" title="No machines yet.">Add one above; it needs key-auth SSH from this machine.</Empty>}
          onRetry={() => hosts.refresh()}
        >
          {(rows) => (
            <div className="list">
              {rows.map((host) => (
                <MachineRow
                  key={host.id}
                  host={host}
                  generation={gen}
                  onChanged={() => { hosts.refresh(); generation.refresh() }}
                  active={activeHost === host.id}
                  onManage={setHost}
                />
              ))}
            </div>
          )}
        </AsyncBoundary>
      </div>
    </section>
  )
}
