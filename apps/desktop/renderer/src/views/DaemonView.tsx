import { useEffect } from 'react'
import type { DaemonStatus } from '@prism/contracts'
import { AsyncBoundary, Banner, Button, Card, Row, Stack, Stat } from '../components/Ui'
import { useAsync, useTask } from '../useAsync'

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
  const actionError = startTask.error ?? stopTask.error

  return (
    <Stack gap="normal">
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
              <Card
                title={tone.headline}
                description={tone.detail ?? 'Local daemon supervised by Prism.'}
                tone={tone.tone}
                action={
                  <Row gap="tight" align="end">
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
                      disabled={!canStop || stopTask.running}
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
                  </Row>
                }
              >
                {actionError !== null ? (
                  <Banner tone="error" title="Action failed">
                    {actionError.message}
                  </Banner>
                ) : null}
                <div className="stats">
                  <Stat label="State" value={current.state} tone={tone.tone} />
                  <Stat label="Endpoint" value={current.endpoint ?? '—'} />
                  <Stat label="PID" value={current.pid === null ? '—' : String(current.pid)} />
                  <Stat label="Attempt" value={String(current.attempt)} />
                </div>
              </Card>
              <Card title="Details" description="Process facts for debugging restarts.">
                <DaemonDetails status={current} />
              </Card>
            </>
          )
        }}
      </AsyncBoundary>
    </Stack>
  )
}

function DaemonDetails({ status }: { readonly status: DaemonStatus }): JSX.Element {
  return (
    <dl className="kv">
      <div>
        <dt>State</dt>
        <dd>{status.state}</dd>
      </div>
      <div>
        <dt>Endpoint</dt>
        <dd>{status.endpoint ?? '—'}</dd>
      </div>
      <div>
        <dt>PID</dt>
        <dd>{status.pid ?? '—'}</dd>
      </div>
      <div>
        <dt>Attempt</dt>
        <dd>{status.attempt}</dd>
      </div>
      <div>
        <dt>Started at</dt>
        <dd>{status.startedAt === null ? '—' : formatTimestamp(status.startedAt)}</dd>
      </div>
      <div>
        <dt>Last exit</dt>
        <dd>{describeExit(status.lastExit)}</dd>
      </div>
      <div>
        <dt>Last error</dt>
        <dd>{status.lastError ?? '—'}</dd>
      </div>
    </dl>
  )
}
