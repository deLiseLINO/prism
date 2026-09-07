import { useEffect, useState, type KeyboardEvent } from 'react'
import { createPortal } from 'react-dom'
import type { ProviderView } from '@prism/contracts'
import { Banner, Button, Field, Select, TextInput, Toggle } from './Ui'
import { useTask, describeError } from '../useAsync'
import { ApiError, api, type ProviderWrite } from '../api'

const WIRE_OPTIONS: readonly { readonly value: string; readonly label: string }[] = [
  { value: 'codex', label: 'codex' },
  { value: 'antigravity', label: 'antigravity' },
  { value: 'responses', label: 'responses' },
  { value: 'messages', label: 'messages' },
  { value: 'chat', label: 'chat' },
]

const BASE_URL_WIRES: readonly string[] = ['responses', 'messages', 'chat']

export interface ProviderModalProps {
  readonly existing: ProviderView | null
  readonly generation: number
  readonly onCancel: () => void
  readonly onSaved: (id: string) => void
}

export function ProviderModal({ existing, generation, onCancel, onSaved }: ProviderModalProps): JSX.Element {
  const [id, setId] = useState(existing?.id ?? '')
  const [wire, setWire] = useState<string>(existing?.wire ?? 'codex')
  const [baseURL, setBaseURL] = useState(existing?.baseURL ?? '')
  const [models, setModels] = useState((existing?.models ?? []).join(', '))
  const [enabled, setEnabled] = useState<boolean>(existing?.enabled ?? true)
  const [credential, setCredential] = useState('')
  const [credentialSet, setCredentialSet] = useState<boolean>(existing?.credential.state === 'set')
  const [apiKeyRef, setApiKeyRef] = useState('')
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

  function splitList(value: string): string[] {
    return value
      .split(',')
      .map((entry) => entry.trim())
      .filter((entry) => entry.length > 0)
  }

  async function save(): Promise<void> {
    if (invalid) return
    const trimmedBase = baseURL.trim()
    const trimmedKeyRef = apiKeyRef.trim()
    const write: ProviderWrite = {
      id: id.trim(),
      wire,
      ...(trimmedBase === '' ? {} : { baseURL: trimmedBase }),
      ...(trimmedKeyRef === '' ? {} : { apiKeyRef: trimmedKeyRef }),
      models: splitList(models),
      disabledModels: existing?.disabledModels ?? [],
      ...(existing?.syncedModels === undefined || existing.syncedModels === null ? {} : { syncedModels: [...existing.syncedModels] }),
      enabled,
      ...(existing?.pool === undefined || existing.pool === null ? {} : { pool: existing.pool }),
      ...(existing?.modelSettings === undefined ? {} : { modelSettings: { ...existing.modelSettings } }),
      ...(credential === '' ? {} : { credential }),
      expectedGeneration: generation,
    }
    const ok = await task.run(() =>
      existing === null ? api.createProvider(write) : api.replaceProvider(existing.id, write),
    )
    if (ok === undefined) return
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
              <span className="badge badge--muted">{existing === null ? wire : existing.wire}</span>
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
            <div className="pmod-grid">
              <Field label="ID" htmlFor="prov-id" hint="Unique key. Required.">
                <TextInput id="prov-id" value={id} onChange={setId} disabled={existing !== null} />
                {idMissing ? <p className="meta">ID is required.</p> : null}
              </Field>
              <Field label="Wire" htmlFor="prov-wire">
                <Select<string>
                  id="prov-wire"
                  value={wire}
                  onChange={setWire}
                  options={[...WIRE_OPTIONS]}
                  disabled={existing !== null}
                />
              </Field>
            </div>
            <Field
              label="Base URL"
              htmlFor="prov-base"
              hint={existing === null ? 'Required for responses, messages, and chat wires.' : 'Empty keeps the current value.'}
            >
              <TextInput id="prov-base" value={baseURL} onChange={setBaseURL} />
              {baseURLMissing ? <p className="meta">Base URL is required for responses, messages, and chat wires.</p> : null}
            </Field>
          </section>
          <section className="msm-sec">
            <div className="msm-sec-label">
              Models
              <span className="msm-sec-meta">
                <span className="msm-k">Count</span>
                <span className="msm-v num">{splitList(models).length}</span>
              </span>
            </div>
            <Field label="Model list" htmlFor="prov-models" hint="Comma-separated. Outbound routing keys; inbound alias targets use these.">
              <TextInput id="prov-models" value={models} onChange={setModels} />
            </Field>
          </section>
          <section className="msm-sec">
            <div className="msm-sec-label">Credential</div>
            <Field
              label="Credential"
              htmlFor="prov-cred"
              hint={`Paste once; the field is cleared after submit. Currently ${credentialSet ? 'set' : 'unset'}.`}
            >
              <TextInput
                id="prov-cred"
                type="password"
                value={credential}
                onChange={setCredential}
                autoComplete="off"
                spellCheck={false}
                placeholder={credentialSet ? '••••••• (set)' : 'paste credential'}
              />
            </Field>
            <Field label="Credential reference" htmlFor="prov-keyref" hint="Optional keyring identifier.">
              <TextInput id="prov-keyref" value={apiKeyRef} onChange={setApiKeyRef} autoComplete="off" />
            </Field>
          </section>
          <section className="msm-sec">
            <div className="msm-sec-label">State</div>
            <div className="msm-row">
              <span>
                <span className="msm-row-name">Provider enabled</span>
                <span className="msm-row-desc">Disabled providers are skipped by routing</span>
              </span>
              <span className="msm-row-right">
                <Toggle checked={enabled} onChange={setEnabled} label="Provider enabled" />
              </span>
            </div>
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

