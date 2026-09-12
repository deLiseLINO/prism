import type { CSSProperties, ReactNode } from 'react'
import type { DaemonStatus, ProvidersView, ProviderView } from '@prism/contracts'
import { AsyncBoundary, Banner, Empty, Toggle } from '../components/Ui'
import { useAsync, useTask, describeError, type AsyncState, type UseAsyncResult } from '../useAsync'
import { api } from '../api'
import { bridge } from '../bridge'

const CONTEXT_PRESETS: readonly { readonly value: number; readonly label: string }[] = [
  { value: 32000, label: '32k' },
  { value: 128000, label: '128k' },
  { value: 256000, label: '256k' },
  { value: 400000, label: '400k' },
  { value: 1000000, label: '1M' },
]

function formatClock(iso: string): string {
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return iso
  return `${date.toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit', timeZone: 'UTC' })}Z`
}

function daemonChip(status: AsyncState<DaemonStatus>): {
  readonly tone: 'ok' | 'warn' | 'danger' | 'muted'
  readonly label: string
  readonly pulse: boolean
} {
  if (status.kind !== 'ready')
    return { tone: 'muted', label: 'checking…', pulse: false }
  const state = status.value.state
  if (state === 'ready')
    return { tone: 'ok', label: 'operational', pulse: true }
  if (state === 'failed')
    return { tone: 'danger', label: 'failed', pulse: false }
  if (state === 'idle' || state === 'stopped' || state === 'quitting')
    return { tone: 'muted', label: state, pulse: false }
  return { tone: 'warn', label: state, pulse: false }
}

function daemonSub(status: AsyncState<DaemonStatus>): JSX.Element {
  if (status.kind === 'error')
    return <>daemon status unavailable, retry from the Daemon view</>
  if (status.kind !== 'ready') return <>reading daemon status…</>
  const value = status.value
  if (value.state !== 'ready') {
    return (
      <>
        daemon {value.state}, attempt{' '}
        <span className="num">{value.attempt}</span>
      </>
    )
  }
  return (
    <>
      daemon ready
      {value.endpoint === null ? (
        ''
      ) : (
        <>
          {' '}
          at <span className="num">{value.endpoint}</span>
        </>
      )}
      , attempt <span className="num">{value.attempt}</span>
      {value.startedAt === null ? (
        ''
      ) : (
        <>
          , up since <span className="num">{formatClock(value.startedAt)}</span>
        </>
      )}
    </>
  )
}

function eligibleSidecarModels(providers: readonly ProviderView[]): string[] {
  const ids: string[] = []
  for (const provider of providers) {
    if (provider.enabled === false) continue
    for (const model of provider.models ?? []) {
      if ((provider.disabledModels ?? []).includes(model)) continue
      if (provider.modelSettings?.[model]?.imageInput) ids.push(`${provider.id}/${model}`)
    }
  }
  return ids.sort((a, b) => a.localeCompare(b))
}

function VisionSidecarCard({
  providers,
}: {
  readonly providers: UseAsyncResult<ProvidersView>
}): ReactNode {
  const task = useTask()
  const save = async (enabled: boolean, target: string): Promise<void> => {
    const ready = providers.state.kind === 'ready' ? providers.state.value : null
    if (ready === null) return
    await task.run(() =>
      api.visionSidecar({
        enabled,
        ...(target === '' ? {} : { target }),
        expectedGeneration: ready.generation,
      }),
    )
    providers.refresh()
  }

  return (
    <section className="panel card ov-card" style={{ '--i': 1 } as CSSProperties}>
      <div className="ov-head">
        <div className="ov-head-l">
          <span className="ov-ic">
            <svg width="18" height="18"><use href="#i-eye" /></svg>
          </span>
          <div>
            <div className="ov-title">Vision sidecar</div>
            <p className="ov-sub">
              describes images with a vision model before they reach text-only models
            </p>
          </div>
        </div>
        <AsyncBoundary<ProvidersView>
          state={providers.state}
          loadingLabel="Loading sidecar…"
          empty={null}
          onRetry={providers.refresh}
        >
          {(all) => (
            <Toggle
              checked={all.visionSidecar.enabled === true}
              onChange={(next) => void save(next, all.visionSidecar.target ?? '')}
              label={all.visionSidecar.enabled === true ? 'Enabled' : 'Disabled'}
              disabled={task.running}
            />
          )}
        </AsyncBoundary>
      </div>
      <AsyncBoundary<ProvidersView>
        state={providers.state}
        loadingLabel="Loading models…"
        empty={null}
        onRetry={providers.refresh}
      >
        {(all) => {
          const eligible = eligibleSidecarModels(all.providers)
          const current = all.visionSidecar.target ?? ''
          const enabled = all.visionSidecar.enabled === true
          return (
            <div className="ov-body">
              {task.error !== null ? (
                <Banner tone="error" title="Sidecar change failed">
                  {describeError(task.error)}
                </Banner>
              ) : null}
              {eligible.length === 0 ? (
                <Empty title="No vision models configured.">
                  Enable image input on a model in Providers to make it eligible.
                </Empty>
              ) : (
                <div className="prov-mlist">
                  {eligible.map((id) => {
                    const selected = id === current
                    return (
                      <div
                        key={id}
                        className={`prov-mline prov-mline--click${selected ? ' prov-prow--sel' : ''}`}
                        role="button"
                        tabIndex={0}
                        aria-pressed={selected}
                        onClick={() => void save(true, id)}
                        onKeyDown={(event) => {
                          if (event.key !== 'Enter' && event.key !== ' ') return
                          event.preventDefault()
                          void save(true, id)
                        }}
                      >
                        <div className="prov-mline-l">
                          <span className="prov-mline-dot" aria-hidden="true" />
                          <span className="prov-mline-name num">{id}</span>
                        </div>
                        {selected ? <span className="badge badge--ok">sidecar</span> : null}
                      </div>
                    )
                  })}
                </div>
              )}
              {enabled && current !== '' && !eligible.includes(current) ? (
                <Banner tone="warn" title="Configured target is no longer eligible.">
                  {current} is disabled or lost image input; pick another model.
                </Banner>
              ) : null}
            </div>
          )
        }}
      </AsyncBoundary>
    </section>
  )
}

function ContextWindowCard({
  providers,
}: {
  readonly providers: UseAsyncResult<ProvidersView>
}): ReactNode {
  const task = useTask()
  const save = async (value: number): Promise<void> => {
    const ready = providers.state.kind === 'ready' ? providers.state.value : null
    if (ready === null) return
    await task.run(() =>
      api.contextWindow({
        contextWindow: value,
        expectedGeneration: ready.generation,
      }),
    )
    providers.refresh()
  }

  return (
    <section className="panel card ov-card" style={{ '--i': 2 } as CSSProperties}>
      <div className="ov-head">
        <div className="ov-head-l">
          <span className="ov-ic">
            <svg width="18" height="18"><use href="#i-gauge" /></svg>
          </span>
          <div>
            <div className="ov-title">Default context window</div>
            <p className="ov-sub">
              applies to every model without its own override
            </p>
          </div>
        </div>
      </div>
      <AsyncBoundary<ProvidersView>
        state={providers.state}
        loadingLabel="Loading default…"
        empty={null}
        onRetry={providers.refresh}
      >
        {(all) => {
          const current = all.contextWindow
          const isPreset = CONTEXT_PRESETS.some((p) => p.value === current)
          return (
            <div className="ov-body">
              {task.error !== null ? (
                <Banner tone="error" title="Default window change failed">
                  {describeError(task.error)}
                </Banner>
              ) : null}
              <div className="msm-seg" role="group" aria-label="Default context window presets">
                {CONTEXT_PRESETS.map((preset) => (
                  <button
                    key={preset.value}
                    type="button"
                    className="msm-seg-btn num"
                    aria-pressed={current === preset.value}
                    disabled={task.running}
                    onClick={() => void save(preset.value)}
                  >
                    {preset.label}
                  </button>
                ))}
              </div>
              <div className="msm-custom">
                <span className="msm-custom-k">Custom</span>
                <input
                  key={current}
                  className="msm-custom-input num"
                  inputMode="numeric"
                  defaultValue={isPreset ? '' : String(current)}
                  placeholder={isPreset ? String(current) : ''}
                  aria-label="Custom default context window"
                  onKeyDown={(event) => {
                    if (event.key !== 'Enter') return
                    const digits = (event.target as HTMLInputElement).value.replace(/[^0-9]/g, '')
                    if (digits !== '' && Number(digits) !== current) void save(Number(digits))
                  }}
                />
                <span className="msm-suffix">tokens</span>
              </div>
            </div>
          )
        }}
      </AsyncBoundary>
    </section>
  )
}

export function OverviewView(): JSX.Element {
  const status = useAsync<DaemonStatus>(() => bridge.daemon.status(), [])
  const providers = useAsync<ProvidersView>(() => api.providers(), [])

  const chip = daemonChip(status.state)

  return (
    <section className="screen" aria-labelledby="h-overview">
      <div className="screen-head" style={{ '--i': 0 } as CSSProperties}>
        <div>
          <h1 id="h-overview">
            <svg width="19" height="19" className="h-ic">
              <use href="#i-gauge" />
            </svg>
            Overview
          </h1>
          <p className="sub">{daemonSub(status.state)}</p>
        </div>
        <div className="head-actions">
          <span className={`chip chip-${chip.tone}`}>
            <span
              className={`dot dot-${chip.tone}${chip.pulse ? ' dot-pulse' : ''}`}
              aria-hidden="true"
            />
            {chip.label}
          </span>
        </div>
      </div>
      <div className="ov-grid">
        <VisionSidecarCard providers={providers} />
        <ContextWindowCard providers={providers} />
      </div>
    </section>
  )
}
