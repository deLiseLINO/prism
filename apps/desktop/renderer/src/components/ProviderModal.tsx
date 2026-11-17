import { useState } from 'react'
import type { ProviderView } from '@prism/contracts'
import { Banner, Button, Field, TextInput } from './Ui'
import { Dialog, DialogSection } from './Patterns'
import { useTask, describeError } from '../useAsync'
import { ApiError, api, type ProviderWrite } from '../api'
import { WIRE_OPTIONS, wireLabel } from '../wires'
import { RenameChanges, useRenameDraft } from './ProviderRename'

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
  const task = useTask()
  const rename = useRenameDraft(existing?.id ?? null, id, generation)

  const idMissing = id.trim() === ''
  const baseURLMissing =
    BASE_URL_WIRES.includes(wire) &&
    baseURL.trim() === '' &&
    (existing === null || (existing.baseURL ?? '') === '')
  const invalid = idMissing || baseURLMissing || rename.problem !== null || (rename.renaming && !rename.ready)

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
    if (existing === null) {
      if ((await task.run(() => api.createProvider(write))) === undefined) return
    } else {
      let current = { id: existing.id, generation }
      if (rename.renaming) {
        const outcome = await task.run(() => api.renameProvider(existing.id, { to: rename.to, expectedGeneration: generation }))
        if (outcome === undefined) return
        if (outcome.kind === 'blocked') {
          rename.blockedOnCommit(outcome.conflicts)
          return
        }
        current = { id: rename.to, generation: outcome.response.generation }
      }
      const fieldsChanged =
        wire !== existing.wire || (trimmedBase !== '' && trimmedBase !== (existing.baseURL ?? '')) || trimmedKey !== ''
      if (!rename.renaming || fieldsChanged) {
        const saved = await task.run(() =>
          api.replaceProvider(current.id, { ...write, id: current.id, expectedGeneration: current.generation }),
        )
        if (saved === undefined) return
      }
    }
    setApiKey('')
    onSaved(id.trim())
  }

  return (
    <Dialog
      label={existing === null ? 'New provider' : 'Edit provider ' + existing.id}
      title={<span className={existing === null ? undefined : 'num'}>{existing === null ? 'New provider' : existing.id}</span>}
      subtitle={<span className="badge badge--muted num">{wireLabel(existing === null ? wire : existing.wire)}</span>}
      onClose={onCancel}
      footer={
        <>
          <span className="ctl-spacer" />
          <Button tone="ghost" onClick={onCancel} disabled={task.running}>
            Cancel
          </Button>
          <Button tone="primary" onClick={() => void save()} disabled={task.running || invalid} busy={task.running}>
            {existing === null ? 'Create provider' : 'Save changes'}
          </Button>
        </>
      }
      error={
        task.error !== null ? (
          <Banner tone="error" title={task.error instanceof ApiError && task.error.code === 'rename_incomplete' ? 'Rename saved, data still moving' : 'Save failed'}>
            {describeError(task.error)}
            {task.error instanceof ApiError && task.error.code === 'stale_generation' ? (
              <Button tone="ghost" size="sm" onClick={() => onSaved(existing?.id ?? id.trim())}>Re-fetch configuration</Button>
            ) : null}
          </Banner>
        ) : undefined
      }
    >
      <DialogSection label="Identity">
        <Field
          label="ID"
          htmlFor="prov-id"
          hint={existing === null ? undefined : rename.problem ?? 'Clients see this provider’s models as <id>/<model>.'}
        >
          <TextInput id="prov-id" value={id} onChange={setId} autoComplete="off" spellCheck={false} />
        </Field>
        <div className="field">
          <span className="dlg__field-label">Wire</span>
          {existing !== null && WIRE_OPTIONS.every((w) => w.value !== wire) ? (
            <span className="badge badge--muted num">{wireLabel(existing.wire)}</span>
          ) : (
            <div className="seg seg--wrap" role="group" aria-label="Provider wire">
              {WIRE_OPTIONS.map((option) => (
                <button
                  key={option.value}
                  type="button"
                  className="seg-btn"
                  aria-pressed={wire === option.value}
                  onClick={() => setWire(option.value)}
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
      </DialogSection>
      {existing !== null && rename.renaming && rename.problem === null ? <RenameChanges from={existing.id} rename={rename} /> : null}
      <DialogSection label="Credential">
        <Field
          label="API key"
          htmlFor="prov-cred"
          hint={existing === null ? undefined : 'Empty keeps the current value.'}
        >
          <TextInput
            id="prov-cred"
            type="password"
            value={apiKey}
            onChange={setApiKey}
            autoComplete="off"
            spellCheck={false}
            placeholder="paste key"
          />
        </Field>
      </DialogSection>
    </Dialog>
  )
}
