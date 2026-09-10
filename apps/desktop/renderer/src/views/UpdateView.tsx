import { useEffect, type CSSProperties } from 'react'
import type { UpdaterState, UpdaterStatus } from '@prism/contracts'
import { AsyncBoundary, Banner, Button } from '../components/Ui'
import { useAsync, useTask } from '../useAsync'
import { bridge } from '../bridge'

const STATE_ORDER: readonly UpdaterState[] = [
  'disabled',
  'idle',
  'checking',
  'available',
  'downloading',
  'downloaded',
  'up-to-date',
  'installing',
  'error',
]

function formatTimestamp(iso: string): string {
  const date = new Date(iso)
  return Number.isNaN(date.getTime()) ? iso : date.toLocaleString()
}

function stateTone(state: UpdaterState): 'ok' | 'warn' | 'error' | 'muted' {
  if (state === 'downloaded' || state === 'up-to-date') return 'ok'
  if (state === 'error') return 'error'
  if (state === 'disabled') return 'muted'
  return 'warn'
}

const CHECKABLE: Record<UpdaterState, boolean> = {
  disabled: false,
  idle: true,
  checking: false,
  available: false,
  downloading: false,
  downloaded: true,
  'up-to-date': true,
  installing: false,
  error: true,
}

export function UpdateView(): JSX.Element {
  const status = useAsync<UpdaterStatus>(
    () => bridge.updater.status(),
    [],
  )

  useEffect(() => {
    const unsubscribe = bridge.updater.onStatus((next) => {
      status.set(next)
    })
    return unsubscribe
  }, [status.set])

  const checkTask = useTask()
  const installTask = useTask()
  const actionError = checkTask.error ?? installTask.error

  const current: UpdaterStatus | null = status.state.kind === 'ready' ? status.state.value : null

  return (
    <section className="screen" aria-labelledby="h-update">
      <div className="screen-head" style={{ '--i': 0 } as CSSProperties}>
        <div>
          <h1 id="h-update">Update</h1>
          <p className="sub">app update channel and lifecycle</p>
        </div>
        <div className="head-actions">
          <Button
            tone="primary"
            onClick={() => {
              void checkTask.run(() => bridge.updater.check())
            }}
            disabled={current === null || !CHECKABLE[current.state]}
            busy={checkTask.running}
          >
            Check for updates
          </Button>
        </div>
      </div>
      <AsyncBoundary<UpdaterStatus>
        state={status.state}
        loadingLabel="Reading update status…"
        empty={<p>no status yet.</p>}
        onRetry={status.refresh}
      >
        {(current) => {
          const canInstall = current.state === 'downloaded'
          return (
            <>
              <div className="daemon-grid">
                <section className="panel card panel-pad" style={{ '--i': 1 } as CSSProperties}>
                  <div className="panel-title">Update state machine</div>
                  <p className="panel-sub">check, download, then explicit install</p>
                  <div className="auth-actions" style={{ marginTop: 20 }}>
                    <Button
                      tone="primary"
                      onClick={() => {
                        void installTask.run(() => bridge.updater.install())
                      }}
                      disabled={!canInstall || installTask.running}
                      busy={installTask.running}
                    >
                      Restart to update{current.downloadedVersion === null ? '' : ` ${current.downloadedVersion}`}
                    </Button>
                    <Button
                      tone="ghost"
                      onClick={() => status.refresh()}
                      disabled={checkTask.running || installTask.running}
                    >
                      Refresh
                    </Button>
                  </div>
                  {actionError !== null ? (
                    <Banner tone="error" title="Action failed">
                      {actionError.message}
                    </Banner>
                  ) : null}
                  {current.state === 'disabled' ? (
                    <p className="note">Updates are disabled: this is a development build or updates were turned off with {`PRISM_UPDATER`}.</p>
                  ) : null}
                  {current.state === 'downloading' && current.progress !== null ? (
                    <p className="note">Downloading {Math.round(current.progress)}%</p>
                  ) : null}
                </section>
                <section className="panel card panel-pad" style={{ '--i': 2 } as CSSProperties}>
                  <div className="panel-title">Channel</div>
                  <div className="kv" style={{ marginTop: 8 }}>
                    <div className="kv-row">
                      <span className="kv-k">State</span>
                      <span className={`kv-v badge badge--${stateTone(current.state)}`}>{current.state}</span>
                    </div>
                    <div className="kv-row">
                      <span className="kv-k">Current version</span>
                      <span className="kv-v num">{current.currentVersion}</span>
                    </div>
                    <div className="kv-row">
                      <span className="kv-k">Available version</span>
                      <span className="kv-v num">{current.availableVersion ?? '—'}</span>
                    </div>
                    <div className="kv-row">
                      <span className="kv-k">Downloaded version</span>
                      <span className="kv-v num">{current.downloadedVersion ?? '—'}</span>
                    </div>
                    <div className="kv-row">
                      <span className="kv-k">Last checked</span>
                      <span className="kv-v num">{current.lastCheckedAt === null ? '—' : formatTimestamp(current.lastCheckedAt)}</span>
                    </div>
                    <div className="kv-row">
                      <span className="kv-k">Error</span>
                      <span className="kv-v num">{current.error ?? '—'}</span>
                    </div>
                  </div>
                </section>
              </div>
              <section className="panel card log-box" style={{ '--i': 3 } as CSSProperties}>
                <div className="panel-title" style={{ padding: '18px 22px 8px' }}>Lifecycle</div>
                <div style={{ padding: '0 22px 18px' }}>
                  {STATE_ORDER.map((state) => (
                    <div key={state} className={`log-line ${state === current.state ? 'ok' : 'muted'}`}>
                      <span className="t num">{state}</span>
                      {state === current.state ? ' ← current' : ''}
                    </div>
                  ))}
                </div>
              </section>
            </>
          )
        }}
      </AsyncBoundary>
    </section>
  )
}
