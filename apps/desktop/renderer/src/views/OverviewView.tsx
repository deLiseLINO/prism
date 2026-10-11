import { useEffect, useRef, useState, type ReactNode } from 'react'
import type { DaemonStatus, ProvidersView, ProviderView } from '@prism/contracts'
import { AsyncBoundary, Banner, Button, Toggle } from '../components/Ui'
import { Metric, PageHeader, Section, SettingRow, StatusChip } from '../components/Patterns'
import { navigateTo } from '../routing'
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

export function eligibleSidecarModels(providers: readonly ProviderView[]): string[] {
  const ids: string[] = []
  for (const provider of providers) {
    if (provider.enabled === false) continue
    for (const model of provider.models ?? []) {
      if ((provider.disabledModels ?? []).includes(model)) continue
      if (provider.resolvedImage?.[model] === true) ids.push(`${provider.id}/${model}`)
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

function DaemonHero({
  status,
  unreachable,
  providers,
}: {
  readonly status: DaemonStatus | null
  readonly unreachable: boolean
  readonly providers: UseAsyncResult<ProvidersView>
}): ReactNode {
  const chip = daemonChip(status, unreachable)
  const endpoint = status === null ? 'unknown' : status.endpoint ?? 'not listening'
  const headline =
    chip.tone === 'ok' ? 'Prism is running'
    : chip.tone === 'danger' ? 'Prism is not responding'
    : chip.tone === 'warn' ? 'Prism is starting'
    : 'Prism is idle'
  const pid = status?.pid
  return (
    <section className={`ov-hero ov-hero--${chip.tone}`} aria-label="Daemon status">
      <div className="ov-hero__status">
        <span className="ov-hero__orb" aria-hidden="true">
          <svg width="22" height="22"><use href="#i-daemon" /></svg>
        </span>
        <div className="ov-hero__text">
          <div className="ov-hero__title">
            {headline}
            <StatusChip tone={chip.tone} pulse={chip.pulse}>{chip.label}</StatusChip>
          </div>
          <p className="ov-hero__sub">
            <span className="num">{status !== null ? status.state : chip.label}</span>
            {' · '}
            <span className="num">{endpoint}</span>
            {pid !== null && pid !== undefined ? (
              <>
                {' · pid '}
                <span className="num">{pid}</span>
              </>
            ) : null}
          </p>
          {status?.lastError ? <p className="ov-error" role="alert">{status.lastError}</p> : null}
        </div>
      </div>
      <div className="ctl-row ov-hero__actions">
        <Button tone="primary" size="lg" onClick={() => navigateTo('providers')}>Manage providers</Button>
        <Button tone="ghost" size="lg" onClick={() => navigateTo('usage')}>Accounts</Button>
        <Button tone="ghost" size="lg" onClick={() => navigateTo('logs')}>View logs</Button>
      </div>
      <AsyncBoundary<ProvidersView> state={providers.state} loadingLabel="Loading providers…" empty={null} onRetry={providers.refresh}>
        {(all) => {
          const enabledProviders = all.providers.filter((p) => p.enabled ?? true).length
          let models = 0
          let off = 0
          for (const p of all.providers) {
            models += (p.models ?? []).length
            off += (p.disabledModels ?? []).length
          }
          return (
            <div className="metrics" style={{ gridColumn: '1 / -1' }}>
              <Metric label="Providers" value={String(all.providers.length)} hint={`${enabledProviders} enabled`} />
              <Metric label="Models" value={String(models - off)} hint={`${off} disabled of ${models}`} />
              <Metric label="Context window" value={all.contextWindow.toLocaleString('en-US')} />
            </div>
          )
        }}
      </AsyncBoundary>
    </section>
  )
}

function VisionSidecarRows({
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
    <Section title="Vision sidecar">
      <AsyncBoundary<ProvidersView>
        state={providers.state}
        loadingLabel="Loading sidecar…"
        empty={null}
        onRetry={providers.refresh}
      >
        {(all) => {
          const eligible = eligibleSidecarModels(all.providers)
          const current = all.visionSidecar.target ?? ''
          const enabled = all.visionSidecar.enabled === true
          return (
            <div className="st-card">
              <SettingRow
                title="Describe images for text-only models"
                control={
                  <Toggle
                    checked={enabled}
                    onChange={(next) => void save(next, current)}
                    label={enabled ? 'Enabled' : 'Disabled'}
                    disabled={task.running}
                  />
                }
              />
              <SettingRow
                title="Vision model"
                control={
                  eligible.length === 0 ? (
                    <span className="note">No vision models configured. Enable image input on a model in Providers to make it eligible.</span>
                  ) : (
                    <div className="ov-set">
                      <ModelSelect
                        ids={eligible}
                        selected={current}
                        onSelect={(id) => void save(true, id)}
                        placeholder="pick a vision model"
                        disabled={task.running}
                      />
                    </div>
                  )
                }
              />
              {task.error !== null ? (
                <div className="lrow-confirm">
                  <Banner tone="error" title="Sidecar change failed">
                    {describeError(task.error)}
                  </Banner>
                </div>
              ) : null}
              {enabled && current !== '' && !eligible.includes(current) ? (
                <div className="lrow-confirm">
                  <Banner tone="warn" title="Configured target is no longer eligible.">
                    {current} is disabled or lost image input; pick another model.
                  </Banner>
                </div>
              ) : null}
            </div>
          )
        }}
      </AsyncBoundary>
    </Section>
  )
}

function ContextWindowRows({
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
    <Section title="Routing defaults">
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
            <div className="st-card">
              <SettingRow
                title="Default context window"
                control={
                  <div className="ov-ctx">
                    <div className="seg" role="group" aria-label="Default context window presets">
                      {CONTEXT_PRESETS.map((preset) => (
                        <button
                          key={preset.value}
                          type="button"
                          className="seg-btn num"
                          aria-pressed={current === preset.value}
                          disabled={task.running}
                          onClick={() => void save(preset.value)}
                        >
                          {preset.label}
                        </button>
                      ))}
                    </div>
                    <label className="ov-ctx__custom">
                      <span className="msm-custom-k">Custom</span>
                      <input
                        key={current}
                        className="text-input text-input--num"
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
                    </label>
                  </div>
                }
              />
              {task.error !== null ? (
                <div className="lrow-confirm">
                  <Banner tone="error" title="Default window change failed">
                    {describeError(task.error)}
                  </Banner>
                </div>
              ) : null}
            </div>
          )
        }}
      </AsyncBoundary>
    </Section>
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
      <PageHeader id="h-overview" icon="#i-home" title="Overview" />
      <DaemonHero status={daemon} unreachable={daemonUnreachable} providers={providers} />
      <ContextWindowRows providers={providers} />
      {visionSidecarVisible ? <VisionSidecarRows providers={providers} /> : null}
    </section>
  )
}
