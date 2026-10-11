import { useEffect, useState } from 'react'
import type { HostView } from '@prism/contracts'
import { api, ApiError } from '../api'
import { useActiveHost } from '../ActiveHost'
import { useExperimentalFlags } from '../experimental'
import { getInstallRun, onInstallSettled, startInstall, useInstallRun } from '../installRuns'
import { AsyncBoundary, Button, Confirm, Empty, Field, TextInput } from '../components/Ui'
import { PageHeader, Section, StatusChip } from '../components/Patterns'
import { describeError, useAsync, useTask } from '../useAsync'

interface MachineRowProps {
  readonly host: HostView
  readonly generation: number
  readonly onChanged: () => void
  readonly active: boolean
  readonly onManage: (host: string) => void
  readonly allowInstall: boolean
}

function MachineRow({ host, generation, onChanged, active, onManage, allowInstall }: MachineRowProps): JSX.Element {

  const [pendingDelete, setPendingDelete] = useState(false)
  const [actionError, setActionError] = useState<string | null>(null)
  const task = useTask()
  const installRun = useInstallRun(host.id)
  const installing = installRun.phase !== null
  const busy = task.running || installing

  async function installDaemon(): Promise<void> {
    setActionError(null)
    await startInstall(host.id)
    if (getInstallRun(host.id).done) onChanged()
  }

  async function remove(): Promise<void> {
    const result = await task.run(() => api.localDeleteHost(host.id, generation))

    if (result === undefined) return
    setPendingDelete(false)
    setActionError(null)
    onChanged()
  }

  if (host.local) {
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

  const ok = host.status === 'ok'
  return (
    <div className="mach-item">
      <div className="lrow lrow--nolead">
        <div className="lrow__main">
          <div className="lrow__title">
            <span>{host.id}</span>
            <StatusChip tone={ok ? 'ok' : 'warn'}>{host.status}</StatusChip>
          </div>
          <span className="lrow__sub lrow__sub--mono lrow__sub--clamp" title={host.detail ?? ''}>{host.detail ?? host.status}</span>
        </div>
        <div className="lrow__side">
          <Button
            tone={active ? 'primary' : 'ghost'}
            onClick={() => onManage(host.id)}
            disabled={host.status !== 'ok'}
          >
            {active ? 'Managing' : 'Manage'}
          </Button>
          {allowInstall && (host.daemonPort ?? 0) === 0 ? (
            <Button
              tone="ghost"
              onClick={() => void installDaemon()}
              disabled={busy}
              busy={installing}
            >
              {installing ? 'Installing…' : installRun.error !== null ? 'Retry install' : 'Install daemon'}
            </Button>
          ) : null}
          <Button
            tone="ghost"
            onClick={() => setPendingDelete(true)}
            disabled={busy}
          >
            Remove
          </Button>
        </div>
      </div>
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
      {pendingDelete ? (
        <div className="lrow-confirm">
          <Confirm
            title={`Remove machine ${host.id}?`}
            detail="The config entry and the SSH tunnel go away. Client configs already written on that machine stay as they are."
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
  const { flags } = useExperimentalFlags()
  const { host: activeHost, setHost } = useActiveHost()
  const [id, setId] = useState('')
  const [address, setAddress] = useState('')
  const [addError, setAddError] = useState<string | null>(null)
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


  const busy = addTask.running
  const gen = generation.state.kind === 'ready' ? generation.state.value : 0

  async function add(): Promise<void> {
    setAddError(null)
    const result = await addTask.run(() => api.localCreateHost({

      id: id.trim(),
      address: address.trim(),
      expectedGeneration: gen,
    }))
    if (result === undefined) return
    if (result.host.status !== 'ok') {
      setAddError(`Saved, but the machine did not answer: ${result.host.detail ?? 'ssh probe failed'}`)
    }
    setId('')
    setAddress('')
    hosts.refresh()
    generation.refresh()
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
      {addError !== null ? (
        <div className="lrow-note lrow-note--card" role="alert">
          <span className="lrow-note__msg">{addError}</span>
          <button type="button" className="lrow-note__close" aria-label="Dismiss" onClick={() => setAddError(null)}>
            ×
          </button>
        </div>
      ) : null}
      <div>
        <AsyncBoundary<readonly HostView[]>
          state={hosts.state}
          loadingLabel="Loading machines…"
          empty={<Empty icon="#i-cube" title="No machines yet.">Add one above; it needs key-auth SSH from this machine.</Empty>}
          onRetry={() => hosts.refresh()}
        >
          {(list) => (
            <div className="list">
              {list.map((host) => (
                <MachineRow
                  key={host.id}
                  host={host}
                  generation={gen}
                  onChanged={() => { hosts.refresh(); generation.refresh() }}
                  active={activeHost === host.id}
                  onManage={setHost}
                  allowInstall={flags.remoteInstall}
                />
              ))}
            </div>
          )}
        </AsyncBoundary>
      </div>
    </section>
  )
}
