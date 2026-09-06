import { useEffect, type CSSProperties } from 'react'
import type { DaemonState, DaemonStatus } from '@prism/contracts'
import { AsyncBoundary, Banner, Button } from '../components/Ui'
import { useAsync, useTask } from '../useAsync'

const STATE_ORDER: readonly DaemonState[] = ['idle', 'starting', 'ready', 'backoff', 'stopped', 'failed', 'stopping', 'quitting']

function formatTimestamp(iso: string): string {
  const date = new Date(iso)
  return Number.isNaN(date.getTime()) ? iso : date.toLocaleString()
}

function describeExit(exit: DaemonStatus['lastExit']): string {
  if (exit === null) return '—'
  const parts: string[] = []
  if (exit.code !== null) parts.push(`exit ${exit.code}`)
  if (exit.signal !== null) parts.push(`signal ${exit.signal}`)
  return parts.length === 0 ? 'no exit recorded' : parts.join(' / ')
}

function describeStatus(status: DaemonStatus): { headline: string; detail: string | null; tone: 'ok' | 'warn' | 'error' } {
  if (status.state === 'ready') {
    return {
      headline: `Daemon ready${status.endpoint === null ? '' : ` on ${status.endpoint}`}`,
      detail: status.startedAt === null ? null : `Started ${formatTimestamp(status.startedAt)}`,
      tone: 'ok',
    }
  }
  if (status.state === 'failed') {
    return {
      headline: 'Daemon failed',
      detail: status.lastError ?? 'unknown error',
      tone: 'error',
    }
  }
  if (status.state === 'starting') {
    return {
      headline: 'Starting…',
      detail: status.lastError,
      tone: 'warn',
    }
  }
  if (status.state === 'backoff') {
    return {
      headline: 'Backing off…',
      detail: status.lastError === null ? `Attempt ${status.attempt} to restart` : status.lastError,
      tone: 'warn',
    }
  }
  return {
    headline: `Daemon ${status.state}`,
    detail: status.lastError,
    tone: 'warn',
  }
}

const STARTABLE: Record<DaemonStatus['state'], boolean> = {
  idle: true,
  starting: false,
  ready: false,
  backoff: false,
  stopping: false,
  stopped: true,
  failed: true,
  quitting: false,
}

const STOPPABLE: Record<DaemonStatus['state'], boolean> = {
  idle: false,
  starting: true,
  ready: true,
  backoff: true,
  stopping: false,
  stopped: false,
  failed: false,
  quitting: false,
}

function stateTone(state: DaemonStatus['state']): 'ok' | 'warn' | 'error' | 'muted' {
  if (state === 'ready') return 'ok'
  if (state === 'failed') return 'error'
  if (state === 'idle' || state === 'stopped') return 'muted'
  return 'warn'
}

function renderMachine(current: DaemonState): JSX.Element {
  const currentIdx = STATE_ORDER.indexOf(current)
  const nodes = STATE_ORDER.slice(0, Math.max(2, currentIdx + 1))
  return (
    <div className="machine">
      {nodes.map((state, i) => (
        <span key={state} style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
          <span className={`node ${i < nodes.length - 1 ? 'visited' : 'current'}`.trim()}>{state}</span>
          {i < nodes.length - 1 ? <span className="m-arrow" aria-hidden="true">→</span> : null}
        </span>
      ))}
    </div>
  )
}

export function DaemonView(): JSX.Element {
  const status = useAsync<DaemonStatus>(
    () => window.prism.daemon.status(),
    [],
  )

  useEffect(() => {
    const unsubscribe = window.prism.daemon.onStatus((next) => {
      status.set(next)
    })
    return unsubscribe
  }, [status.set])

  const startTask = useTask()
  const stopTask = useTask()
  const restartTask = useTask()
  const actionError = startTask.error ?? stopTask.error ?? restartTask.error

  async function restart(): Promise<void> {
    await stopTask.run(() => window.prism.daemon.stop())
    await startTask.run(() => window.prism.daemon.start())
    status.refresh()
  }

  return (
    <section className="screen" aria-labelledby="h-daemon">
      <div className="screen-head" style={{ '--i': 0 } as CSSProperties}>
        <div>
          <h1 id="h-daemon">
            <svg width="19" height="19" className="h-ic">
              <use href="#i-daemon" />
            </svg>
            Daemon
          </h1>
          <p className="sub">supervisor process backing the local router</p>
        </div>
        <div className="head-actions">
          <Button
            tone="primary"
            onClick={() => {
              void restart()
            }}
            disabled={startTask.running || stopTask.running || restartTask.running}
            busy={restartTask.running}
          >
            <svg width="14" height="14">
              <use href="#i-refresh" />
            </svg>
            Restart daemon
          </Button>
        </div>
      </div>
      <AsyncBoundary<DaemonStatus>
        state={status.state}
        loadingLabel="Reading daemon status…"
        empty={<p>no status yet.</p>}
        onRetry={status.refresh}
      >
        {(current) => {
          const tone = describeStatus(current)
          const canStart = STARTABLE[current.state]
          const canStop = STOPPABLE[current.state]
          return (
            <>
              <div className="daemon-grid">
                <section className="panel card panel-pad" style={{ '--i': 1 } as CSSProperties}>
                  <div className="panel-title">Supervisor state machine</div>
                  <p className="panel-sub">boot attempts tracked with exponential backoff</p>
                  <div style={{ marginTop: 16 }}>{renderMachine(current.state)}</div>
                  <div className="auth-actions" style={{ marginTop: 20 }}>
                    <Button
                      tone="primary"
                      onClick={() => {
                        void startTask.run(() => window.prism.daemon.start())
                      }}
                      disabled={!canStart || startTask.running || stopTask.running}
                      busy={startTask.running}
                    >
                      Start
                    </Button>
                    <Button
                      tone="ghost"
                      onClick={() => {
                        void stopTask.run(() => window.prism.daemon.stop())
                      }}
                      disabled={!canStop || startTask.running || stopTask.running}
                      busy={stopTask.running}
                    >
                      Stop
                    </Button>
                    <Button
                      tone="ghost"
                      onClick={() => status.refresh()}
                      disabled={startTask.running || stopTask.running}
                    >
                      Refresh
                    </Button>
                  </div>
                  {actionError !== null ? (
                    <Banner tone="error" title="Action failed">
                      {actionError.message}
                    </Banner>
                  ) : null}
                  {tone.detail !== null ? <p className="note">{tone.detail}</p> : null}
                </section>
                <section className="panel card panel-pad" style={{ '--i': 2 } as CSSProperties}>
                  <div className="panel-title">Process</div>
                  <div className="kv" style={{ marginTop: 8 }}>
                    <div className="kv-row">
                      <span className="kv-k">State</span>
                      <span className={`kv-v badge badge--${stateTone(current.state)}`}>{current.state}</span>
                    </div>
                    <div className="kv-row">
                      <span className="kv-k">PID</span>
                      <span className="kv-v num">{current.pid === null ? '—' : current.pid}</span>
                    </div>
                    <div className="kv-row">
                      <span className="kv-k">Endpoint</span>
                      <span className="kv-v num">{current.endpoint ?? '—'}</span>
                    </div>
                    <div className="kv-row">
                      <span className="kv-k">Started</span>
                      <span className="kv-v num">{current.startedAt === null ? '—' : formatTimestamp(current.startedAt)}</span>
                    </div>
                    <div className="kv-row">
                      <span className="kv-k">Attempt</span>
                      <span className="kv-v num">{current.attempt}</span>
                    </div>
                    <div className="kv-row">
                      <span className="kv-k">Last exit</span>
                      <span className="kv-v num">{describeExit(current.lastExit)}</span>
                    </div>
                    <div className="kv-row">
                      <span className="kv-k">Last error</span>
                      <span className="kv-v num">{current.lastError ?? '—'}</span>
                    </div>
                  </div>
                </section>
              </div>
              <section className="panel card log-box" style={{ '--i': 3 } as CSSProperties}>
                <div className="panel-title" style={{ padding: '18px 22px 8px' }}>Recent logs</div>
                <div style={{ padding: '0 22px 18px' }}>
                  <div className="log-line ok">
                    <span className="t num">{current.startedAt === null ? '—' : formatTimestamp(current.startedAt)}</span>
                    supervisor state <span className="num">{current.state}</span>, attempt{' '}
                    <span className="num">{current.attempt}</span>
                    {current.pid === null ? '' : <>, pid <span className="num">{current.pid}</span></>}
                  </div>
                  {current.lastError !== null ? (
                    <div className="log-line warn">
                      <span className="t num">last</span>
                      {current.lastError}
                    </div>
                  ) : null}
                  {current.lastExit !== null ? (
                    <div className="log-line warn">
                      <span className="t num">exit</span>
                      {describeExit(current.lastExit)}
                    </div>
                  ) : null}
                </div>
              </section>
            </>
          )
        }}
      </AsyncBoundary>
    </section>
  )
}
