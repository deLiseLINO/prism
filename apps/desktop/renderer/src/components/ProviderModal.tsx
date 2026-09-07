import { useEffect, useState, type KeyboardEvent } from 'react'
import { createPortal } from 'react-dom'
import type { ProviderView } from '@prism/contracts'
import { Banner, Button, Field, TextInput } from './Ui'
import { useTask, describeError } from '../useAsync'
import { ApiError, api, type ProviderWrite } from '../api'
import { WIRE_OPTIONS, wireLabel } from '../wires'

const BASE_URL_WIRES: readonly string[] = ['responses', 'messages', 'chat']

export interface ProviderModalProps {
  readonly existing: ProviderView | null
  readonly generation: number
  readonly onCancel: () => void
  readonly onSaved: (id: string) => void
}

export function ProviderModal({ existing, generation, onCancel, onSaved }: ProviderModalProps): JSX.Element {
  const [id, setId] = useState(existing?.id ?? '')
  const [wire, setWire] = useState<string>(existing?.wire ?? 'responses')
  const [baseURL, setBaseURL] = useState(existing?.baseURL ?? '')
  const [apiKey, setApiKey] = useState('')
  const [apiKeySet, setApiKeySet] = useState<boolean>(existing?.credential.state === 'set')
  const task = useTask()

  useEffect(() => {
    const onKey = (event: globalThis.KeyboardEvent): void => {
      if (event.key === 'Escape') onCancel()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onCancel])

  const idMissing = id.trim() === ''
  const baseURLMissing =
    BASE_URL_WIRES.includes(wire) &&
    baseURL.trim() === '' &&
    (existing === null || (existing.baseURL ?? '') === '')
  const invalid = idMissing || baseURLMissing

  async function save(): Promise<void> {
    if (invalid) return
    const trimmedBase = baseURL.trim()
    const trimmedKey = apiKey.trim()
    const write: ProviderWrite = {
      id: id.trim(),
      wire,
      ...(trimmedBase === '' ? {} : { baseURL: trimmedBase }),
      models: existing?.models ?? [],
      disabledModels: existing?.disabledModels ?? [],
      ...(existing?.syncedModels === undefined || existing.syncedModels === null ? {} : { syncedModels: [...existing.syncedModels] }),
      ...(existing?.pool === undefined || existing.pool === null ? {} : { pool: existing.pool }),
      ...(existing?.modelSettings === undefined ? {} : { modelSettings: { ...existing.modelSettings } }),
      ...(trimmedKey === '' ? {} : { credential: trimmedKey }),
      expectedGeneration: generation,
    }
    const ok = await task.run(() =>
      existing === null ? api.createProvider(write) : api.replaceProvider(existing.id, write),
    )
    if (ok === undefined) return
    setApiKeySet(trimmedKey !== '' || apiKeySet)
    setApiKey('')
    onSaved(id.trim())
  }

  function onDialogKey(event: KeyboardEvent): void {
    if (event.key === 'Escape') {
      event.stopPropagation()
      onCancel()
    }
  }

  return createPortal(
    <div className="msm-veil" role="presentation" onClick={onCancel}>
      <section
        className="msm card"
        role="dialog"
        aria-modal="true"
        aria-label={existing === null ? 'New provider' : 'Edit provider ' + existing.id}
        onClick={(event) => event.stopPropagation()}
        onKeyDown={onDialogKey}
      >
        <header className="msm-head">
          <div>
            <div className="msm-title num">{existing === null ? 'New provider' : existing.id}</div>
            <div className="msm-sub">
              <span className="badge badge--muted num">{wireLabel(existing === null ? wire : existing.wire)}</span>
              <span className="msm-hint">{existing === null ? 'Create' : 'Edit provider'}</span>
            </div>
          </div>
          <button type="button" className="ibtn" aria-label="Close" onClick={onCancel}>
            <svg width="13" height="13" viewBox="0 0 14 14" fill="none" stroke="currentColor" strokeWidth="1.2" strokeLinecap="round"><path d="M2.5 2.5l9 9M11.5 2.5l-9 9" /></svg>
          </button>
        </header>
        <div className="msm-body">
          <section className="msm-sec">
            <div className="msm-sec-label">Identity</div>
            <Field label="ID" htmlFor="prov-id">
              <TextInput id="prov-id" value={id} onChange={setId} disabled={existing !== null} />
            </Field>
            <div className="pmod-wire">
              <span className="pmod-wire-label">Wire</span>
              {existing !== null && WIRE_OPTIONS.every((w) => w.value !== wire) ? (
                <span className="badge badge--muted num">{wireLabel(existing.wire)}</span>
              ) : (
                <div className="msm-seg" role="group" aria-label="Provider wire">
                  {WIRE_OPTIONS.map((option) => (
                    <button
                      key={option.value}
                      type="button"
                      className="msm-seg-btn"
                      aria-pressed={wire === option.value}
                      onClick={() => setWire(option.value)}
                      disabled={existing !== null}
                    >
                      {option.label}
                    </button>
                  ))}
                </div>
              )}
              <select id="prov-wire" className="sr-only" value={wire} onChange={(e) => setWire(e.target.value)} tabIndex={-1} aria-hidden="true">
                {WIRE_OPTIONS.map((option) => (
                  <option key={option.value} value={option.value}>{option.label}</option>
                ))}
              </select>
            </div>
            <Field
              label="Base URL"
              htmlFor="prov-base"
              hint={existing === null ? undefined : 'Empty keeps the current value.'}
            >
              <TextInput id="prov-base" value={baseURL} onChange={setBaseURL} />
            </Field>
          </section>
          <section className="msm-sec">
            <div className="msm-sec-label">Credential</div>
            <Field
              label="API key"
              htmlFor="prov-cred"
              hint={apiKeySet ? 'Already set. Empty keeps it.' : undefined}
            >
              <TextInput
                id="prov-cred"
                type="password"
                value={apiKey}
                onChange={setApiKey}
                autoComplete="off"
                spellCheck={false}
                placeholder={apiKeySet ? 'paste new key' : 'paste key'}
              />
            </Field>
          </section>
        </div>
        <footer className="msm-foot">
          <span className="msm-spacer" />
          <Button tone="ghost" size="sm" onClick={onCancel} disabled={task.running}>
            Cancel
          </Button>
          <Button tone="primary" size="sm" onClick={() => void save()} disabled={task.running || invalid} busy={task.running}>
            {existing === null ? 'Create provider' : 'Save changes'}
          </Button>
        </footer>
        {task.error !== null ? (
          <div className="pmod-error">
            <Banner tone="error" title="Save failed">
              {describeError(task.error)}
              {task.error instanceof ApiError && task.error.code === 'stale_generation' ? (
                <Button tone="ghost" size="sm" onClick={() => onSaved(id.trim())}>Re-fetch configuration</Button>
              ) : null}
            </Banner>
          </div>
        ) : null}
      </section>
    </div>,
    document.body,
  )
}
