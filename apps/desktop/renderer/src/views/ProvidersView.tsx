import { useState, type CSSProperties } from 'react'
import type { ProviderView, ProvidersView } from '@prism/contracts'
import { AsyncBoundary, Banner, Button, Confirm, Empty, Field, Row, SearchInput, Select, Stack, TextInput, Toggle } from '../components/Ui'
import { useAsync, useTask, describeError } from '../useAsync'
import { ApiError, api, type ProviderWrite } from '../api'

const WIRE_OPTIONS: readonly { readonly value: string; readonly label: string }[] = [
  { value: 'codex', label: 'codex' },
  { value: 'antigravity', label: 'antigravity' },
  { value: 'responses', label: 'responses' },
  { value: 'messages', label: 'messages' },
  { value: 'chat', label: 'chat' },
]

const BASE_URL_WIRES: readonly string[] = ['responses', 'messages', 'chat']

interface EditorProps {
  readonly existing: ProviderView | null
  readonly generation: number
  readonly onSaved: () => void
  readonly onCancelled: () => void
}

function buildWrite(provider: ProviderView, generation: number, patch: Partial<ProviderWrite>): ProviderWrite {
  const base: ProviderWrite = {
    id: provider.id,
    wire: provider.wire,
    models: [...(provider.models ?? [])],
    disabledModels: [...(provider.disabledModels ?? [])],
    ...(provider.enabled === undefined ? {} : { enabled: provider.enabled }),
    expectedGeneration: generation,
  }
  return { ...base, ...patch }
}

function ProviderEditor({ existing, generation, onSaved, onCancelled }: EditorProps): JSX.Element {
  const [id, setId] = useState(existing?.id ?? '')
  const [wire, setWire] = useState<string>(existing?.wire ?? 'codex')
  const [baseURL, setBaseURL] = useState(existing?.baseURL ?? '')
  const [defaultModel, setDefaultModel] = useState(existing?.defaultModel ?? '')
  const [models, setModels] = useState((existing?.models ?? []).join(', '))
  const [disabledModels, setDisabledModels] = useState((existing?.disabledModels ?? []).join(', '))
  const [enabled, setEnabled] = useState<boolean>(existing?.enabled ?? true)
  const [credential, setCredential] = useState('')
  const [credentialSet, setCredentialSet] = useState<boolean>(existing?.credential.state === 'set')
  const [apiKeyRef, setApiKeyRef] = useState('')
  const task = useTask()

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
    const trimmedDefault = defaultModel.trim()
    const write: ProviderWrite = {
      id: id.trim(),
      wire,
      ...(trimmedBase === '' ? {} : { baseURL: trimmedBase }),
      ...(trimmedKeyRef === '' ? {} : { apiKeyRef: trimmedKeyRef }),
      ...(trimmedDefault === '' ? {} : { defaultModel: trimmedDefault }),
      models: splitList(models),
      disabledModels: splitList(disabledModels),
      enabled,
      ...(existing?.pool === undefined || existing.pool === null ? {} : { pool: existing.pool }),
      ...(credential === '' ? {} : { credential }),
      expectedGeneration: generation,
    }
    const result = await task.run(() =>
      existing === null ? api.createProvider(write) : api.replaceProvider(existing.id, write),
    )
    if (result === undefined) return
    setCredential('')
    setCredentialSet(credential !== '' || credentialSet)
    onSaved()
  }

  return (
    <section className="panel card panel-pad" style={{ '--i': 2 } as CSSProperties}>
      <h3 className="panel-title">{existing === null ? 'New provider' : `Edit ${existing.id}`}</h3>
      <div className="stack stack--normal">
        <div className="grid-2">
          <Field label="ID" htmlFor="prov-id" hint="Unique key. Required.">
            <TextInput id="prov-id" value={id} onChange={setId} disabled={existing !== null} />
            {idMissing ? (
              <p className="meta">ID is required.</p>
            ) : null}
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
          <Field
            label="Base URL"
            htmlFor="prov-base"
            hint={
              existing === null
                ? 'Required for responses, messages, and chat wires.'
                : 'Empty keeps the current value.'
            }
          >
            <TextInput id="prov-base" value={baseURL} onChange={setBaseURL} />
            {baseURLMissing ? (
              <p className="meta">Base URL is required for responses, messages, and chat wires.</p>
            ) : null}
          </Field>
          <Field
            label="Default model"
            htmlFor="prov-default"
            hint={existing === null ? undefined : 'Empty keeps the current value.'}
          >
            <TextInput id="prov-default" value={defaultModel} onChange={setDefaultModel} />
          </Field>
          <Field
            label="Models"
            htmlFor="prov-models"
            hint="Comma-separated. Outbound routing keys; inbound alias targets use these."
          >
            <TextInput id="prov-models" value={models} onChange={setModels} />
          </Field>
          <Field label="Disabled models" htmlFor="prov-disabled" hint="Subset of Models that this provider will skip.">
            <TextInput id="prov-disabled" value={disabledModels} onChange={setDisabledModels} />
          </Field>
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
              placeholder={credentialSet ? '•••••••• (set)' : 'paste credential'}
            />
          </Field>
          <Field label="Credential reference" htmlFor="prov-keyref" hint="Optional keyring identifier.">
            <TextInput id="prov-keyref" value={apiKeyRef} onChange={setApiKeyRef} autoComplete="off" />
          </Field>
        </div>
        <Row gap="loose" align="start">
          <Toggle checked={enabled} onChange={setEnabled} label="Provider enabled" />
        </Row>
        <Row gap="tight" align="start">
          <Button tone="primary" size="sm" onClick={() => void save()} disabled={task.running || invalid} busy={task.running}>
            {existing === null ? 'Create provider' : 'Save changes'}
          </Button>
          <Button tone="ghost" size="sm" onClick={onCancelled} disabled={task.running}>
            Cancel
          </Button>
        </Row>
        {task.error !== null ? (
          <Banner tone="error" title="Save failed">
            {describeError(task.error)}
            {task.error instanceof ApiError && task.error.code === 'stale_generation' ? (
              <>
                : config changed underneath.
                <Button tone="ghost" size="sm" onClick={onSaved}>Re-fetch configuration</Button>
              </>
            ) : null}
          </Banner>
        ) : null}
      </div>
    </section>
  )
}

interface ProviderRowProps {
  readonly provider: ProviderView
  readonly generation: number
  readonly index: number
  readonly onChanged: () => void
}

function ProviderRow({ provider, generation, index, onChanged }: ProviderRowProps): JSX.Element {
  const [editing, setEditing] = useState(false)
  const [confirming, setConfirming] = useState(false)
  const task = useTask()

  async function toggle(): Promise<void> {
    const ok = await task.run(() =>
      api.replaceProvider(provider.id, buildWrite(provider, generation, { enabled: !(provider.enabled ?? true) })),
    )
    if (ok === undefined) return
    onChanged()
  }

  async function toggleModel(model: string): Promise<void> {
    const disabled = new Set(provider.disabledModels ?? [])
    if (disabled.has(model)) disabled.delete(model)
    else disabled.add(model)
    const ok = await task.run(() =>
      api.replaceProvider(provider.id, buildWrite(provider, generation, { disabledModels: [...disabled] })),
    )
    if (ok === undefined) return
    onChanged()
  }

  async function remove(): Promise<void> {
    const ok = await task.run(() => api.deleteProvider(provider.id, generation))
    if (ok === undefined) return
    setConfirming(false)
    onChanged()
  }

  if (editing) {
    return (
      <ProviderEditor
        existing={provider}
        generation={generation}
        onSaved={() => {
          setEditing(false)
          onChanged()
        }}
        onCancelled={() => setEditing(false)}
      />
    )
  }

  const disabledModels = provider.disabledModels ?? []
  const models = provider.models ?? []
  const credentialTone = provider.credential.state === 'set' ? 'ok' : 'muted'

  return (
    <section className="panel card divide" style={{ '--i': index % 3 } as CSSProperties}>
      <div className="prov">
        <div>
          <h3 className="prov-name">{provider.id}</h3>
          <p className="prov-wire">
            {provider.wire}
            {provider.baseURL ? ` · ${provider.baseURL}` : ''}
          </p>
        </div>
        <div>
          {models.length > 0 ? (
            <div className="prov-models">
              {models.map((model) => {
                const off = disabledModels.includes(model)
                const isDefault = model === provider.defaultModel
                const classes = ['m-chip']
                if (isDefault) classes.push('m-chip-default')
                if (off) classes.push('m-chip-off')
                return (
                  <span key={model} className={classes.join(' ')}>{model}</span>
                )
              })}
            </div>
          ) : (
            <p className="meta">No models listed.</p>
          )}
        </div>
        <div className="prov-side">
          <span className={`badge badge--${credentialTone}`}>credential {provider.credential.state}</span>
          <Toggle
            checked={provider.enabled ?? true}
            onChange={() => void toggle()}
            label={(provider.enabled ?? true) ? 'Enabled' : 'Disabled'}
            disabled={task.running}
          />
        </div>
      </div>
      <div className="panel-pad" style={{ paddingTop: 0 }}>
        {disabledModels.length > 0 ? (
          <p className="meta">{disabledModels.length} disabled</p>
        ) : null}
        {models.length > 0 ? (
          <div className="model-toggles" aria-label={`Models for ${provider.id}`}>
            {models.map((model) => (
              <Toggle
                key={model}
                checked={!disabledModels.includes(model)}
                onChange={() => void toggleModel(model)}
                label={model}
                disabled={task.running}
              />
            ))}
          </div>
        ) : null}
        <Row gap="tight" align="start">
          <Button tone="ghost" size="sm" onClick={() => setEditing(true)} disabled={task.running}>
            Edit
          </Button>
          {confirming ? (
            <Confirm
              title={`Delete ${provider.id}?`}
              detail="The provider leaves the config immediately."
              confirmLabel="Delete"
              busy={task.running}
              onCancel={() => setConfirming(false)}
              onConfirm={() => void remove()}
            />
          ) : (
            <Button tone="danger" size="sm" onClick={() => setConfirming(true)} disabled={task.running}>
              Delete
            </Button>
          )}
        </Row>
        {task.error !== null ? (
          <Banner tone="error" title="Mutation failed">
            {describeError(task.error)}
            {task.error instanceof ApiError && task.error.code === 'stale_generation' ? (
              <Button tone="ghost" size="sm" onClick={onChanged}>Re-fetch configuration</Button>
            ) : null}
          </Banner>
        ) : null}
      </div>
    </section>
  )
}

export function ProvidersView(): JSX.Element {
  const providers = useAsync<ProvidersView>(() => api.providers(), [])
  const [creating, setCreating] = useState(false)
  const [query, setQuery] = useState('')

  return (
    <section className="screen" aria-labelledby="h-providers">
      <div className="screen-head" style={{ '--i': 0 } as CSSProperties}>
        <div>
          <h1 id="h-providers">
            <svg width="19" height="19" className="h-ic"><use href="#i-plug" /></svg>
            Providers
          </h1>
          <p className="sub">wires, models and credential state for every outbound connection</p>
        </div>
        <div className="head-actions">
          <SearchInput
            id="provider-search"
            value={query}
            onChange={setQuery}
            placeholder="Filter by id, wire, or model"
          />
          <Button tone="primary" size="sm" onClick={() => setCreating(true)}>
            New provider
          </Button>
        </div>
      </div>
      {creating ? (
        <AsyncBoundary<ProvidersView>
          state={providers.state}
          loadingLabel="Preparing form…"
          empty={null}
        >
          {(list) => (
            <ProviderEditor
              existing={null}
              generation={list.generation}
              onSaved={() => {
                setCreating(false)
                providers.refresh()
              }}
              onCancelled={() => setCreating(false)}
            />
          )}
        </AsyncBoundary>
      ) : null}
      <AsyncBoundary<ProvidersView>
        state={providers.state}
        loadingLabel="Loading providers…"
        empty={<Empty title="No providers configured." />}
        onRetry={() => providers.refresh()}
      >
        {(list) => {
          const needle = query.trim().toLowerCase()
          const filtered = list.providers.filter(
            (provider) =>
              needle === '' ||
              provider.id.toLowerCase().includes(needle) ||
              provider.wire.toLowerCase().includes(needle) ||
              (provider.models ?? []).some((model) => model.toLowerCase().includes(needle)),
          )
          if (filtered.length === 0) {
            return <Empty title={list.providers.length === 0 ? 'No providers configured.' : 'No providers match the current filter.'} />
          }
          return (
            <Stack gap="normal">
              {filtered.map((provider, index) => (
                <ProviderRow
                  key={provider.id}
                  provider={provider}
                  generation={list.generation}
                  index={index}
                  onChanged={() => providers.refresh()}
                />
              ))}
            </Stack>
          )
        }}
      </AsyncBoundary>
    </section>
  )
}
