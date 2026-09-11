import { useEffect, useState } from 'react'
import type { CSSProperties } from 'react'
import type { HostView } from '@prism/contracts'
import { api, ApiError } from '../api'
import { useActiveHost } from '../ActiveHost'
import { useExperimentalFlags } from '../experimental'
import { getInstallRun, onInstallSettled, startInstall, useInstallRun } from '../installRuns'
import { AsyncBoundary, Button, Confirm, Empty, Field, TextInput } from '../components/Ui'
import { describeError, useAsync, useTask } from '../useAsync'

interface MachineRowProps {
  readonly host: HostView
  readonly generation: number
  readonly onChanged: () => void
  readonly active: boolean
  readonly onManage: (host: string) => void
  readonly allowInstall: boolean
}

function statusDot(status: string): string {
  return status === 'ok' ? 'dot-ok' : 'dot-warn'
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
      <div className="int-row">
        <div className="int-cell int-name">{host.id}</div>
        <div className="int-cell int-status int-status--managed">
          <span className="dot dot-ok" aria-hidden="true" />
          this machine
        </div>
        <div className="int-cell int-pathbox">
          <span className="int-path">config lives here</span>
        </div>
        <div className="int-cell int-actions">
          <Button tone={active ? 'primary' : 'ghost'} size="sm" onClick={() => onManage('local')}>
            {active ? 'Managing' : 'Manage here'}
          </Button>
        </div>
      </div>
    )
  }

  return (
    <div className="int-row-wrap">
      <div className="int-row">
        <div className="int-cell int-name">{host.id}</div>
        <div className={`int-cell int-status ${host.status === 'ok' ? 'int-status--managed' : 'int-status--drift'}`}>
          <span className={`dot ${statusDot(host.status)}`} aria-hidden="true" />
          {host.status}
        </div>
        <div className="int-cell int-pathbox">
          <span className="int-path" title={host.detail ?? ''}>{host.detail ?? host.status}</span>
        </div>
        <div className="int-cell int-actions">
          <Button
            tone={active ? 'primary' : 'ghost'}
            size="sm"
            onClick={() => onManage(host.id)}
            disabled={host.status !== 'ok'}
          >
            {active ? 'Managing' : 'Manage'}
          </Button>
          {allowInstall && (host.daemonPort ?? 0) === 0 ? (
            <Button
              tone="ghost"
              size="sm"
              onClick={() => void installDaemon()}
              disabled={busy}
              busy={installing}
            >
              {installing ? 'Installing…' : installRun.error !== null ? 'Retry install' : 'Install prismd'}
            </Button>
          ) : null}

          <Button

            tone="ghost"
            size="sm"
            onClick={() => setPendingDelete(true)}
            disabled={busy}
          >
            Remove
          </Button>
        </div>

      </div>
      {task.error !== null ? (
        <div className="int-refusal" role="alert">
          <span className="int-refusal__msg">{describeError(task.error)}</span>
        </div>
      ) : null}
      {task.error === null && installRun.error !== null ? (
        <div className="int-refusal" role="alert">
          <span className="int-refusal__msg">{installRun.error}</span>
        </div>
      ) : null}
      {pendingDelete ? (
        <Confirm
          title={`Remove machine ${host.id}?`}
          detail="The config entry and the SSH tunnel go away. Client configs already written on that machine stay as they are."
          confirmLabel="Remove"
          busy={busy}
          onCancel={() => setPendingDelete(false)}
          onConfirm={() => void remove()}
        />
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
      <div className="screen-head" style={{ '--i': 0 } as CSSProperties}>
        <div>
          <h1 id="h-machines">
            <svg width="19" height="19" className="h-ic">
              <use href="#i-cube" />
            </svg>
            Machines
          </h1>
        </div>
      </div>
      <div className="machine-add" style={{ '--i': 1 } as CSSProperties}>
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
            placeholder="user@host"
            disabled={busy}
          />
        </Field>
        <Button tone="primary" onClick={() => void add()} disabled={!canAdd} busy={busy}>
          Add machine
        </Button>
      </div>
      {addError !== null ? (
        <div className="int-refusal" role="alert">
          <span className="int-refusal__msg">{addError}</span>
          <button type="button" className="int-refusal__close" aria-label="Dismiss" onClick={() => setAddError(null)}>
            ×
          </button>
        </div>
      ) : null}
      <AsyncBoundary<readonly HostView[]>
        state={hosts.state}
        loadingLabel="Loading machines…"
        empty={<Empty title="No machines yet.">Add one above; it needs key-auth SSH from this machine.</Empty>}
        onRetry={() => hosts.refresh()}
      >
        {(list) => (
          <div className="int-list" style={{ '--i': 2 } as CSSProperties}>
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
    </section>
  )
}
