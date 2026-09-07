import { useEffect, useState, type KeyboardEvent, type MouseEvent } from 'react'
import { createPortal } from 'react-dom'
import type { ModelSettingsView } from '@prism/contracts'
import { Button, Toggle } from './Ui'

const CONTEXT_PRESETS: readonly { readonly value: number; readonly label: string }[] = [
  { value: 32000, label: '32k' },
  { value: 128000, label: '128k' },
  { value: 256000, label: '256k' },
  { value: 400000, label: '400k' },
  { value: 1000000, label: '1M' },
]

const EFFORTS: readonly string[] = ['minimal', 'low', 'medium', 'high', 'xhigh']

function fmt(n: number): string {
  return n.toLocaleString('en-US')
}

export interface ModelSettingsDraft {
  readonly contextWindow: number | null
  readonly imageInput: boolean
  readonly effortsOverride: boolean
  readonly efforts: ReadonlySet<string>
}

export function draftFromSettings(settings: ModelSettingsView | undefined): ModelSettingsDraft {
  return {
    contextWindow: settings?.contextWindow ?? null,
    imageInput: settings?.imageInput ?? false,
    effortsOverride: (settings?.reasoningEfforts?.length ?? 0) > 0,
    efforts: new Set(settings?.reasoningEfforts ?? []),
  }
}

export function draftToSettings(draft: ModelSettingsDraft): ModelSettingsView {
  return {
    ...(draft.contextWindow === null ? {} : { contextWindow: draft.contextWindow }),
    imageInput: draft.imageInput,
    ...(draft.effortsOverride ? { reasoningEfforts: [...draft.efforts] } : {}),
  }
}

export interface ModelSettingsModalProps {
  readonly providerId: string
  readonly model: string
  readonly isNew: boolean
  readonly fallbackContextWindow: number
  readonly initial: ModelSettingsView | undefined
  readonly busy: boolean
  readonly onCancel: () => void
  readonly onSave: (draft: ModelSettingsDraft) => void
}

export function ModelSettingsModal({
  providerId,
  model,
  isNew,
  fallbackContextWindow,
  initial,
  busy,
  onCancel,
  onSave,
}: ModelSettingsModalProps): JSX.Element {
  const [draft, setDraft] = useState<ModelSettingsDraft>(() => draftFromSettings(initial))
  const [custom, setCustom] = useState(() => {
    const value = initial?.contextWindow
    const preset = CONTEXT_PRESETS.find((p) => p.value === value)
    return preset === undefined && value !== undefined ? String(value) : ''
  })

  useEffect(() => {
    const onKey = (event: globalThis.KeyboardEvent): void => {
      if (event.key === 'Escape') onCancel()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onCancel])

  function setContextOverride(on: boolean): void {
    if (!on) {
      setDraft((d) => ({ ...d, contextWindow: null }))
      setCustom('')
      return
    }
    setDraft((d) => ({ ...d, contextWindow: d.contextWindow ?? fallbackContextWindow }))
  }

  function pickPreset(value: number): void {
    setDraft((d) => ({ ...d, contextWindow: value }))
    setCustom('')
  }

  function pickEffort(effort: string): void {
    setDraft((d) => {
      const efforts = new Set(d.efforts)
      if (!d.effortsOverride) {
        efforts.clear()
        efforts.add(effort)
        return { ...d, effortsOverride: true, efforts }
      }
      if (efforts.has(effort)) efforts.delete(effort)
      else efforts.add(effort)
      return { ...d, efforts }
    })
  }

  function onCustomInput(raw: string): void {
    const digits = raw.replace(/[^0-9]/g, '').slice(0, 9)
    setCustom(digits)
    setDraft((d) => ({ ...d, contextWindow: digits === '' ? null : Number(digits) }))
  }

  function onDialogKey(event: KeyboardEvent): void {
    if (event.key === 'Escape') {
      event.stopPropagation()
      onCancel()
    }
  }

  function rowToggle(event: MouseEvent<HTMLDivElement>, apply: () => void): void {
    if ((event.target as HTMLElement).closest('.toggle') !== null) return
    apply()
  }

  const effective = draft.contextWindow ?? fallbackContextWindow
  const ctxOver = draft.contextWindow !== null
  const isPreset = CONTEXT_PRESETS.some((p) => p.value === draft.contextWindow)
  const saveDisabled = busy || (draft.effortsOverride && draft.efforts.size === 0)

  return createPortal(
    <div className="msm-veil" role="presentation" onClick={onCancel}>
      <section
        className="msm card"
        role="dialog"
        aria-modal="true"
        aria-label={isNew ? 'New model ' + model : 'Model settings ' + model}
        onClick={(event) => event.stopPropagation()}
        onKeyDown={onDialogKey}
      >
        <header className="msm-head">
          <div>
            <div className="msm-title num">{model}</div>
            <div className="msm-sub">
              <span className="badge badge--muted">{providerId}</span>
              <span className="msm-hint">{isNew ? 'New model' : 'Model settings'}</span>
            </div>
          </div>
          <button type="button" className="ibtn" aria-label="Close" onClick={onCancel}>
            <svg width="13" height="13" viewBox="0 0 14 14" fill="none" stroke="currentColor" strokeWidth="1.2" strokeLinecap="round"><path d="M2.5 2.5l9 9M11.5 2.5l-9 9" /></svg>
          </button>
        </header>
        <div className="msm-body">
          <section className="msm-sec">
            <div className="msm-sec-label">Input</div>
            <div
              className="msm-row msm-row--click"
              onClick={(event) =>
                rowToggle(event, () => setDraft((d) => ({ ...d, imageInput: !d.imageInput })))
              }
            >
              <span>
                <span className="msm-row-name">Image input</span>
                <span className="msm-row-desc">Screenshots and attachments</span>
              </span>
              <span className="msm-row-right">
                <Toggle
                  checked={draft.imageInput}
                  onChange={(imageInput) => setDraft((d) => ({ ...d, imageInput }))}
                  label="Image input"
                  visuallyHidden
                />
              </span>
            </div>
          </section>
          <section className="msm-sec">
            <div className="msm-sec-label">
              Context window
              <span className="msm-sec-meta">
                <span className="msm-tag num">{ctxOver ? 'custom' : 'default'}</span>
                <span className="msm-k">Effective</span>
                <span className="msm-v num">{fmt(effective)}</span>
              </span>
            </div>
            <div className={'msm-ctx' + (ctxOver ? ' msm-ctx--over' : '')}>
              <div
                className="msm-override msm-row--click"
                onClick={(event) => rowToggle(event, () => setContextOverride(!ctxOver))}
              >
                <span>
                  <span className="msm-row-name">Override context window</span>
                  <span className="msm-row-desc">Set a specific limit for this model</span>
                </span>
                <span className="msm-row-right">
                  <Toggle
                    checked={ctxOver}
                    onChange={setContextOverride}
                    label="Override context window"
                    visuallyHidden
                  />
                </span>
              </div>
              <div className="msm-seg" role="group" aria-label="Context window presets">
                {CONTEXT_PRESETS.map((preset) => (
                  <button
                    key={preset.value}
                    type="button"
                    className="msm-seg-btn num"
                    aria-pressed={draft.contextWindow === preset.value}
                    onClick={() => pickPreset(preset.value)}
                  >
                    {preset.label}
                  </button>
                ))}
              </div>
              <div className="msm-custom">
                <span className="msm-custom-k">Custom</span>
                <input
                  className="msm-custom-input num"
                  value={custom}
                  onChange={(event) => onCustomInput(event.target.value)}
                  inputMode="numeric"
                  placeholder={isPreset ? String(draft.contextWindow) : '512000'}
                  aria-label="Custom context window"
                />
                <span className="msm-suffix">tokens</span>
              </div>
              <p className="msm-note">
                Effective <b className="num">{fmt(effective)} tokens</b>
                {ctxOver ? ' · applies to this model' : ' · falls back to the global default'}
              </p>
            </div>
          </section>
          <section className="msm-sec">
            <div className="msm-sec-label">Reasoning</div>
            <div className={'msm-rea' + (draft.effortsOverride ? ' msm-rea--over' : '')}>
              <div
                className="msm-override msm-row--click"
                onClick={(event) =>
                  rowToggle(event, () =>
                    setDraft((d) => ({
                      ...d,
                      effortsOverride: !d.effortsOverride,
                      efforts: !d.effortsOverride ? d.efforts : new Set<string>(),
                    })),
                  )
                }
              >
                <span>
                  <span className="msm-row-name">Override reasoning efforts</span>
                  <span className="msm-row-desc">Restrict which levels are offered</span>
                </span>
                <span className="msm-row-right">
                  <Toggle
                    checked={draft.effortsOverride}
                    onChange={(effortsOverride) =>
                      setDraft((d) => ({
                        ...d,
                        effortsOverride,
                        efforts: effortsOverride ? d.efforts : new Set<string>(),
                      }))
                    }
                    label="Override reasoning efforts"
                    visuallyHidden
                  />
                </span>
              </div>
              <div className="msm-efforts" role="group" aria-label="Reasoning efforts">
                {EFFORTS.map((effort) => (
                  <button
                    key={effort}
                    type="button"
                    className={'msm-effort num' + (draft.efforts.has(effort) ? ' msm-effort--on' : '')}
                    aria-pressed={draft.efforts.has(effort)}
                    onClick={() => pickEffort(effort)}
                  >
                    {effort}
                  </button>
                ))}
              </div>
              <p className="msm-note">
                {draft.effortsOverride
                  ? 'Pick the efforts agents may use.'
                  : 'All efforts available. Click an effort to override.'}
              </p>
            </div>
          </section>
        </div>
        <footer className="msm-foot">
          <Button tone="ghost" size="sm" onClick={() => setDraft(draftFromSettings(undefined))} disabled={busy}>
            Reset to defaults
          </Button>
          <span className="msm-spacer" />
          <Button tone="ghost" size="sm" onClick={onCancel} disabled={busy}>
            Cancel
          </Button>
          <Button tone="primary" size="sm" onClick={() => onSave(draft)} disabled={saveDisabled} busy={busy}>
            Save
          </Button>
        </footer>
      </section>
    </div>,
    document.body,
  )
}
