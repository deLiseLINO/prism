import { useEffect, useRef, useState, type CSSProperties, type ReactNode } from 'react'
import type { DaemonStatus, ProvidersView, ProviderView } from '@prism/contracts'
import { AsyncBoundary, Banner, Empty, Toggle } from '../components/Ui'
import { useAsync, useTask, describeError, type UseAsyncResult } from '../useAsync'
import { api } from '../api'
import { useExperimentalFlags } from '../experimental'

const CONTEXT_PRESETS: readonly { readonly value: number; readonly label: string }[] = [
  { value: 32000, label: '32k' },
  { value: 128000, label: '128k' },
  { value: 256000, label: '256k' },
  { value: 400000, label: '400k' },
  { value: 1000000, label: '1M' },
]

function daemonChip(
  status: DaemonStatus | null,
  unreachable: boolean,
): {
  readonly tone: 'ok' | 'warn' | 'danger' | 'muted'
  readonly label: string
  readonly pulse: boolean
} {
  if (unreachable) return { tone: 'danger', label: 'unreachable', pulse: false }
  if (status === null) return { tone: 'muted', label: 'checking…', pulse: false }
  const state = status.state
  if (state === 'ready') return { tone: 'ok', label: 'operational', pulse: true }
  if (state === 'failed') return { tone: 'danger', label: 'failed', pulse: false }
  if (state === 'idle' || state === 'stopped' || state === 'quitting')
    return { tone: 'muted', label: state, pulse: false }
  return { tone: 'warn', label: state, pulse: false }
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

export function ModelSelect({
  ids,
  selected,
  onSelect,
  placeholder,
  disabled = false,
}: {
  readonly ids: readonly string[]
  readonly selected: string
  readonly onSelect: (id: string) => void
  readonly placeholder: string
  readonly disabled?: boolean
}): ReactNode {
  const [open, setOpen] = useState(false)
  const rootRef = useRef<HTMLDivElement | null>(null)
  const btnRef = useRef<HTMLButtonElement | null>(null)
  const menuRef = useRef<HTMLDivElement | null>(null)
  const effectiveOpen = open && !disabled

  useEffect(() => {
    if (!effectiveOpen) return
    const onPointerDown = (event: PointerEvent): void => {
      const root = rootRef.current
      if (root !== null && !root.contains(event.target as Node)) setOpen(false)
    }
    const onKeyDown = (event: KeyboardEvent): void => {
      if (event.key !== 'Escape') return
      setOpen(false)
      btnRef.current?.focus()
    }
    window.addEventListener('pointerdown', onPointerDown)
    window.addEventListener('keydown', onKeyDown)
    return () => {
      window.removeEventListener('pointerdown', onPointerDown)
      window.removeEventListener('keydown', onKeyDown)
    }
  }, [effectiveOpen])

  const focusOption = (from: EventTarget | null, delta: 1 | -1): void => {
    const options = Array.from(
      menuRef.current?.querySelectorAll<HTMLButtonElement>('.vsd__opt') ?? [],
    )
    if (options.length === 0) return
    const index = options.findIndex((option) => option === from)
    const next =
      index === -1
        ? Math.max(0, options.findIndex((option) => option.dataset.id === selected))
        : Math.min(options.length - 1, Math.max(0, index + delta))
    options[next === -1 ? 0 : next]?.focus()
  }

  return (
    <div className={`vsd${effectiveOpen ? ' vsd--open' : ''}`} ref={rootRef}>
      <button
        ref={btnRef}
        type="button"
        className="vsd__btn"
        aria-haspopup="listbox"
        aria-expanded={effectiveOpen}
        disabled={disabled}
        onClick={() => {
          setOpen((wasOpen) => !wasOpen)
        }}
        onKeyDown={(event) => {
          if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return
          event.preventDefault()
          if (!effectiveOpen) setOpen(true)
          focusOption(event.currentTarget, event.key === 'ArrowDown' ? 1 : -1)
        }}
      >
        <span className="vsd__cur">{selected !== '' ? selected : placeholder}</span>
        <svg
          className="vsd__chev"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          strokeWidth="2.4"
          aria-hidden="true"
        >
          <path d="m6 9 6 6 6-6" />
        </svg>
      </button>
      <div className="vsd__menu" role="listbox" ref={menuRef}>
        {ids.map((id) => (
          <button
            key={id}
            type="button"
            className="vsd__opt"
            role="option"
            aria-selected={id === selected}
            data-id={id}
            onClick={() => {
              setOpen(false)
              btnRef.current?.focus()
              onSelect(id)
            }}
            onKeyDown={(event) => {
              if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return
              event.preventDefault()
              focusOption(event.currentTarget, event.key === 'ArrowDown' ? 1 : -1)
            }}
          >
            <span className="vsd__opt-dot" aria-hidden="true" />
            <span className="vsd__opt-name">{id}</span>
          </button>
        ))}
      </div>
    </div>
  )
}

function DaemonCard({
  status,
  unreachable,
}: {
  readonly status: DaemonStatus | null
  readonly unreachable: boolean
}): ReactNode {
  const chip = daemonChip(status, unreachable)
  return (
    <section className="panel card ov-card" style={{ '--i': 1, gridColumn: '1 / -1' } as CSSProperties}>
      <div className="ov-head">
        <div className="ov-head-l">
          <span className="ov-ic">
            <svg width="18" height="18"><use href="#i-daemon" /></svg>
          </span>
          <div>
            <div className="ov-title">Daemon</div>
            <p className="ov-sub">
              local prismd supervised by the desktop app
            </p>
          </div>
        </div>
        <span className={`chip chip-${chip.tone}`}>
          <span
            className={`dot dot-${chip.tone}${chip.pulse ? ' dot-pulse' : ''}`}
            aria-hidden="true"
          />
          {chip.label}
        </span>
      </div>
      <div className="ov-body">
        <div className="kv">
          <div className="kv-row">
            <span className="kv-k">state</span>
            <span className="kv-v num">{status !== null ? status.state : chip.label}</span>
          </div>
          <div className="kv-row">
            <span className="kv-k">endpoint</span>
            <span className="kv-v num">
              {status === null ? 'unknown' : status.endpoint ?? 'not listening'}
            </span>
          </div>
        </div>
      </div>
    </section>
  )
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
    <section className="panel card ov-card" style={{ '--i': 2 } as CSSProperties}>
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
                <>
                  <ModelSelect
                    ids={eligible}
                    selected={current}
                    onSelect={(id) => void save(true, id)}
                    placeholder="pick a vision model"
                    disabled={task.running}
                  />
                  <p className="ov-sub">pick the model that describes images for text-only models</p>
                </>
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
    <section className="panel card ov-card" style={{ '--i': 3 } as CSSProperties}>
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

export function OverviewView({
  daemon,
  daemonUnreachable,
}: {
  readonly daemon: DaemonStatus | null
  readonly daemonUnreachable: boolean
}): JSX.Element {
  const providers = useAsync<ProvidersView>(() => api.providers(), [])
  const { flags } = useExperimentalFlags()
  const visionSidecarVisible = flags.visionSidecar

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
          <p className="sub">daemon state and default context window</p>
        </div>
      </div>
      <div className="ov-grid">
        <DaemonCard status={daemon} unreachable={daemonUnreachable} />
        {visionSidecarVisible ? <VisionSidecarCard providers={providers} /> : null}
        <ContextWindowCard providers={providers} />
      </div>
    </section>
  )
}
