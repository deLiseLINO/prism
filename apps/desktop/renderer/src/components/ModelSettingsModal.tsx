import { useState, type MouseEvent } from 'react'
import type { ModelSettingsView } from '@prism/contracts'
import { Button, Toggle } from './Ui'
import { Dialog, DialogSection } from './Patterns'
import { OVERRIDE_WIRES, isOverridableWire, wireLabel, type OverrideWire } from '../wires'

const CONTEXT_PRESETS: readonly { readonly value: number; readonly label: string }[] = [
  { value: 32000, label: '32k' },
  { value: 128000, label: '128k' },
  { value: 256000, label: '256k' },
  { value: 400000, label: '400k' },
  { value: 1000000, label: '1M' },
]

const EFFORTS: readonly string[] = ['minimal', 'low', 'medium', 'high', 'xhigh', 'max']

export type ContextSource = 'listing' | 'catalog' | 'global'

export function contextCaption(source: ContextSource, n: number): string {
  return `${source} ${fmt(n)}`
}

function fmt(n: number): string {
  return n.toLocaleString('en-US')
}

export interface ModelSettingsDraft {
  readonly contextWindow: number | null
  readonly imageInput: boolean
  readonly imageTouched: boolean
  readonly effortsOverride: boolean
  readonly efforts: ReadonlySet<string>
  readonly wireOverride: OverrideWire | null
}

export function draftFromSettings(
  settings: ModelSettingsView | undefined,
  resolvedImage = false,
): ModelSettingsDraft {
  return {
    contextWindow: settings?.contextWindow ?? null,
    imageInput: settings?.imageInput ?? resolvedImage,
    imageTouched: settings?.imageInput !== undefined,
    effortsOverride: (settings?.reasoningEfforts?.length ?? 0) > 0,
    efforts: new Set(settings?.reasoningEfforts ?? []),
    wireOverride: settings?.wire ?? null,
  }
}

export function draftToSettings(draft: ModelSettingsDraft, providerWire: string): ModelSettingsView {
  return {
    ...(draft.contextWindow === null ? {} : { contextWindow: draft.contextWindow }),
    ...(draft.imageTouched ? { imageInput: draft.imageInput } : {}),
    ...(draft.effortsOverride ? { reasoningEfforts: [...draft.efforts] } : {}),
    ...(draft.wireOverride === null || draft.wireOverride === providerWire ? {} : { wire: draft.wireOverride }),
  }
}

export interface ModelSettingsModalProps {
  readonly providerId: string
  readonly model: string
  readonly isNew: boolean
  readonly fallbackContextWindow: number
  readonly providerWire: string
  readonly contextSource?: ContextSource
  readonly resolvedImage?: boolean
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
  providerWire,
  contextSource = 'global',
  resolvedImage = false,
  initial,
  busy,
  onCancel,
  onSave,
}: ModelSettingsModalProps): JSX.Element {
  const [draft, setDraft] = useState<ModelSettingsDraft>(() => draftFromSettings(initial, resolvedImage))
  const [custom, setCustom] = useState(() => {
    const value = initial?.contextWindow
    const preset = CONTEXT_PRESETS.find((p) => p.value === value)
    return preset === undefined && value !== undefined ? String(value) : ''
  })

  function setContextOverride(on: boolean): void {
    if (!on) {
      setDraft((d) => ({ ...d, contextWindow: null }))
      setCustom('')
      return
    }
    setDraft((d) => ({ ...d, contextWindow: d.contextWindow ?? fallbackContextWindow }))
  }

  function setWireOverride(on: boolean): void {
    setDraft((d) => ({
      ...d,
      wireOverride: on ? (d.wireOverride ?? OVERRIDE_WIRES.find((w) => w.value !== providerWire)?.value ?? null) : null,
    }))
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

  function rowToggle(event: MouseEvent<HTMLDivElement>, apply: () => void): void {
    if ((event.target as HTMLElement).closest('.toggle') !== null) return
    apply()
  }

  const effective = draft.contextWindow ?? fallbackContextWindow
  const ctxOver = draft.contextWindow !== null
  const wireOver = draft.wireOverride !== null
  const isPreset = CONTEXT_PRESETS.some((p) => p.value === draft.contextWindow)
  const saveDisabled = busy || (draft.effortsOverride && draft.efforts.size === 0)

  return (
    <Dialog
      label={isNew ? 'New model ' + model : 'Model settings ' + model}
      title={<span className="num">{model}</span>}
      subtitle={<span className="badge badge--muted">{providerId}</span>}
      onClose={onCancel}
      footer={
        <>
          <Button tone="ghost" onClick={() => setDraft(draftFromSettings(initial, resolvedImage))} disabled={busy}>
            Reset to defaults
          </Button>
          <span className="ctl-spacer" />
          <Button tone="ghost" onClick={onCancel} disabled={busy}>
            Cancel
          </Button>
          <Button tone="primary" onClick={() => onSave(draft)} disabled={saveDisabled} busy={busy}>
            Save
          </Button>
        </>
      }
    >
          <DialogSection label="Input">
            <div
              className="msm-row msm-row--click"
              onClick={(event) =>
                rowToggle(event, () => setDraft((d) => ({ ...d, imageTouched: true, imageInput: !d.imageInput })))
              }
            >
              <span>
                <span className="msm-row-name">Image input</span>
              </span>
              <span className="msm-row-right">
                <Toggle
                  checked={draft.imageInput}
                  onChange={(imageInput) => setDraft((d) => ({ ...d, imageTouched: true, imageInput }))}
                  label="Image input"
                  visuallyHidden
                />
              </span>
            </div>
          </DialogSection>
          <DialogSection label="Context window" meta={<>
                <span className="msm-tag num">{ctxOver ? 'custom' : 'default'}</span>
                <span className="msm-k">Effective</span>
                <span className="msm-v num">{fmt(effective)}</span>
              </>}>
            <div className={'msm-ctx' + (ctxOver ? ' msm-ctx--over' : '')}>
              <div
                className="msm-override msm-row--click"
                onClick={(event) => rowToggle(event, () => setContextOverride(!ctxOver))}
              >
                <span>
                  <span className="msm-row-name">Override context window</span>
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
              <div className="seg seg--wrap" role="group" aria-label="Context window presets">
                {CONTEXT_PRESETS.map((preset) => (
                  <button
                    key={preset.value}
                    type="button"
                    className="seg-btn num"
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
                  className="text-input text-input--num"
                  value={custom}
                  onChange={(event) => onCustomInput(event.target.value)}
                  inputMode="numeric"
                  placeholder={String(fallbackContextWindow)}
                  aria-label="Custom context window"
                />
                <span className="msm-suffix">tokens</span>
              </div>
              <p className="msm-note">
                Effective <b className="num">{fmt(effective)} tokens</b>
                {ctxOver
                  ? ' · applies to this model'
                  : ` · ${contextCaption(contextSource, fallbackContextWindow)}`}
              </p>
            </div>
          </DialogSection>
          <DialogSection label="Reasoning">
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
            </div>
          </DialogSection>
          {isOverridableWire(providerWire) ? (
            <DialogSection label="API" meta={<>
                  <span className="msm-tag num">{wireOver ? 'custom' : 'default'}</span>
                  <span className="msm-k">Effective</span>
                  <span className="msm-v num">{wireLabel(draft.wireOverride ?? providerWire)}</span>
                </>}>
              <div className={'msm-ctx' + (wireOver ? ' msm-ctx--over' : '')}>
                <div
                  className="msm-override msm-row--click"
                  onClick={(event) => rowToggle(event, () => setWireOverride(!wireOver))}
                >
                  <span>
                    <span className="msm-row-name">Override provider API</span>
                  </span>
                  <span className="msm-row-right">
                    <Toggle
                      checked={wireOver}
                      onChange={setWireOverride}
                      label="Override provider API"
                      visuallyHidden
                    />
                  </span>
                </div>
                <div className="seg seg--wrap" role="group" aria-label="API">
                  {OVERRIDE_WIRES.map((wire) => (
                    <button
                      key={wire.value}
                      type="button"
                      className="seg-btn"
                      aria-pressed={draft.wireOverride === wire.value}
                      onClick={() => setDraft((d) => ({ ...d, wireOverride: wire.value }))}
                    >
                      {wire.label}
                    </button>
                  ))}
                </div>
              </div>
            </DialogSection>
          ) : null}
    </Dialog>
  )
}
