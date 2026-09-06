import { useMemo, useState, type CSSProperties, type ReactNode } from 'react'
import type { ModelSettingsView, ProviderView, ProvidersView as ProvidersViewData } from '@prism/contracts'
import { AsyncBoundary, Banner, Button, Confirm, Empty, Field, Row, SearchInput, Select, TextInput, Toggle } from '../components/Ui'
import { useAsync, useTask, describeError } from '../useAsync'
import { ApiError, api, type ProviderWrite } from '../api'
import { ModelSettingsModal, draftToSettings, type ModelSettingsDraft } from '../components/ModelSettingsModal'

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
  readonly onSaved: (id: string) => void
  readonly onCancelled: () => void
}

function buildWrite(provider: ProviderView, generation: number, patch: Partial<ProviderWrite>): ProviderWrite {
  const base: ProviderWrite = {
    id: provider.id,
    wire: provider.wire,
    models: [...(provider.models ?? [])],
    disabledModels: [...(provider.disabledModels ?? [])],
    ...(provider.syncedModels === undefined || provider.syncedModels === null ? {} : { syncedModels: [...provider.syncedModels] }),
    ...(provider.enabled === undefined ? {} : { enabled: provider.enabled }),
    ...(provider.baseURL === undefined || provider.baseURL === null ? {} : { baseURL: provider.baseURL }),
    ...(provider.pool === undefined || provider.pool === null ? {} : { pool: provider.pool }),
    modelSettings: cloneModelSettings(provider.modelSettings),
    expectedGeneration: generation,
  }
  return { ...base, ...patch }
}

function cloneModelSettings(
  settings: Readonly<Record<string, ModelSettingsView>> | undefined,
): Record<string, ModelSettingsView> {
  const next: Record<string, ModelSettingsView> = {}
  for (const [model, value] of Object.entries(settings ?? {})) {
    next[model] = { ...value }
  }
  return next
}

function stripRemovedModels(
  settings: Readonly<Record<string, ModelSettingsView>> | undefined,
  models: readonly string[],
): Record<string, ModelSettingsView> {
  const allowed = new Set(models)
  const next: Record<string, ModelSettingsView> = {}
  for (const [model, value] of Object.entries(settings ?? {})) {
    if (allowed.has(model)) next[model] = { ...value }
  }
  return next
}

function ProviderEditor({ existing, generation, onSaved, onCancelled }: EditorProps): JSX.Element {
  const [id, setId] = useState(existing?.id ?? '')
  const [wire, setWire] = useState<string>(existing?.wire ?? 'codex')
  const [baseURL, setBaseURL] = useState(existing?.baseURL ?? '')
  const [models, setModels] = useState((existing?.models ?? []).join(', '))
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
    const write: ProviderWrite = {
      id: id.trim(),
      wire,
      ...(trimmedBase === '' ? {} : { baseURL: trimmedBase }),
      ...(trimmedKeyRef === '' ? {} : { apiKeyRef: trimmedKeyRef }),
      models: splitList(models),
      disabledModels: existing?.disabledModels ?? [],
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
    onSaved(id.trim())
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
            label="Models"
            htmlFor="prov-models"
            hint="Comma-separated. Outbound routing keys; inbound alias targets use these."
          >
            <TextInput id="prov-models" value={models} onChange={setModels} />
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
                <Button tone="ghost" size="sm" onClick={() => onSaved(id.trim())}>Re-fetch configuration</Button>
              </>
            ) : null}
          </Banner>
        ) : null}
      </div>
    </section>
  )
}

interface DetailProps {
  readonly provider: ProviderView
  readonly generation: number
  readonly globalContextWindow: number
  readonly onOptimistic: (id: string, patch: Partial<ProviderView>) => void
  readonly onMutated: () => void
  readonly onEdit: () => void
  readonly onDelete: () => void
  readonly onToggleProvider: (provider: ProviderView) => void
  readonly onRemoveModel: (provider: ProviderView, model: string) => void
}

function ProviderDetail({ provider, generation, globalContextWindow, onOptimistic, onMutated, onEdit, onDelete, onToggleProvider, onRemoveModel }: DetailProps): JSX.Element {
  const [confirming, setConfirming] = useState(false)
  const [addModel, setAddModel] = useState('')
  const [modalModel, setModalModel] = useState<string | null>(null)
  const [modalNew, setModalNew] = useState(false)
  const [modalError, setModalError] = useState<Error | null>(null)
  const [toggleError, setToggleError] = useState<Error | null>(null)
  const [syncError, setSyncError] = useState<Error | null>(null)
  const [syncing, setSyncing] = useState(false)
  const task = useTask()
  const models = provider.models ?? []
  const disabledModels = provider.disabledModels ?? []
  const syncedModels = provider.syncedModels ?? []

  async function mutate(patch: Partial<ProviderWrite>): Promise<void> {
    const ok = await task.run(() => api.replaceProvider(provider.id, buildWrite(provider, generation, patch)))
    if (ok === undefined) return
    onMutated()
  }

  async function toggleModel(model: string): Promise<void> {
    const disabled = new Set(disabledModels)
    if (disabled.has(model)) disabled.delete(model)
    else disabled.add(model)
    const next = [...disabled]
    onOptimistic(provider.id, { disabledModels: next })
    try {
      await api.replaceProvider(provider.id, buildWrite(provider, generation, { disabledModels: next }))
      setToggleError(null)
    } catch (err: unknown) {
      setToggleError(err instanceof Error ? err : new Error(String(err)))
    }
    onMutated()
  }

  async function setAllModelsDisabled(disabled: boolean): Promise<void> {
    if (disabledModels.length === (disabled ? models.length : 0)) return
    const next = disabled ? [...models] : []
    onOptimistic(provider.id, { disabledModels: next })
    try {
      await api.replaceProvider(provider.id, buildWrite(provider, generation, { disabledModels: next }))
      setToggleError(null)
    } catch (err: unknown) {
      setToggleError(err instanceof Error ? err : new Error(String(err)))
    }
    onMutated()
  }

  async function syncModels(): Promise<void> {
    if (syncing) return
    setSyncing(true)
    setSyncError(null)
    try {
      await api.syncProviderModels(provider.id, generation)
    } catch (err: unknown) {
      setSyncError(err instanceof Error ? err : new Error(String(err)))
    }
    setSyncing(false)
    onMutated()
  }

  async function addModelSubmit(): Promise<void> {
    const value = addModel.trim()
    if (value === '' || models.includes(value)) return
    const ok = await task.run(() =>
      api.replaceProvider(provider.id, buildWrite(provider, generation, { models: [...models, value] })),
    )
    if (ok === undefined) return
    setAddModel('')
    onMutated()
  }

  async function saveModelSettings(model: string, draft: ModelSettingsDraft): Promise<void> {
    const settings = cloneModelSettings(provider.modelSettings)
    if (modalNew) {
      settings[model] = draftToSettings(draft)
      const nextModels = models.includes(model) ? [...models] : [...models, model]
      onOptimistic(provider.id, { models: nextModels, modelSettings: settings })
      try {
        await api.replaceProvider(provider.id, buildWrite(provider, generation, { models: nextModels, modelSettings: settings }))
        setModalError(null)
      } catch (err: unknown) {
        setModalError(err instanceof Error ? err : new Error(String(err)))
      }
      onMutated()
      return
    }
    settings[model] = draftToSettings(draft)
    onOptimistic(provider.id, { modelSettings: settings })
    try {
      await api.replaceProvider(provider.id, buildWrite(provider, generation, { modelSettings: settings }))
      setModalError(null)
    } catch (err: unknown) {
      setModalError(err instanceof Error ? err : new Error(String(err)))
    }
    onMutated()
  }

  async function removeModel(model: string): Promise<void> {
    const ok = await task.run(() =>
      api.replaceProvider(provider.id, buildWrite(provider, generation, {
        models: models.filter((m) => m !== model),
        modelSettings: stripRemovedModels(provider.modelSettings, models.filter((m) => m !== model)),
      })),
    )
    if (ok === undefined) return
    onMutated()
  }

  const credentialTone = provider.credential.state === 'set' ? 'ok' : 'muted'
  const settings = provider.modelSettings ?? {}

  return (
    <section className="panel card prov-detail" style={{ '--i': 1 } as CSSProperties}>
      <div className="prov-detail-head">
        <div>
          <div className="prov-detail-title">
            <h2 className="prov-detail-name">{provider.id}</h2>
            <span className={`badge badge--${credentialTone}`}>credential {provider.credential.state}</span>
          </div>
          <p className="prov-detail-wire">
            {provider.wire}
            {provider.baseURL ? ` · ${provider.baseURL}` : ''}
          </p>
        </div>
        <Toggle
          checked={provider.enabled ?? true}
          onChange={() => onToggleProvider(provider)}
          label={(provider.enabled ?? true) ? 'Enabled' : 'Disabled'}
        />
      </div>
      <div className="prov-detail-body">
        <div className="prov-detail-label">
          models
          <span className="num">{models.length}</span>
          <span className="prov-detail-acts">
            <button
              type="button"
              className="ibtn"
              onClick={() => void syncModels()}
              disabled={syncing || task.running}
              aria-label="Refresh models from provider"
              title="Refresh models from provider"
            >
              <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" className={syncing ? 'spin' : ''}><path d="M21 12a9 9 0 1 1-2.64-6.36L21 8"/><path d="M21 3v5h-5"/></svg>
            </button>
            <button
              type="button"
              className="ibtn"
              onClick={() => void setAllModelsDisabled(true)}
              disabled={task.running || models.length === 0 || disabledModels.length === models.length}
              aria-label="Disable all models"
              title="Disable all"
            >
              <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round"><circle cx="12" cy="12" r="9"/><path d="M5.6 5.6l12.8 12.8"/></svg>
            </button>
            <button
              type="button"
              className="ibtn"
              onClick={() => void setAllModelsDisabled(false)}
              disabled={task.running || models.length === 0 || disabledModels.length === 0}
              aria-label="Enable all models"
              title="Enable all"
            >
              <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round"><path d="M20 6L9 17l-5-5"/></svg>
            </button>
            <span className="num">{disabledModels.length} off</span>
          </span>
        </div>
        {models.length > 0 ? (
          <div className="prov-mlist">
            {models.map((model) => {
              const off = disabledModels.includes(model)
              const manual = !syncedModels.includes(model)
              return (
                <div
                  className={`prov-mline${off ? ' prov-mline--off' : ''} prov-mline--click`}
                  key={model}
                  role="button"
                  tabIndex={0}
                  onClick={() => void toggleModel(model)}
                  onKeyDown={(event) => {
                    if (event.key !== 'Enter' && event.key !== ' ') return
                    event.preventDefault()
                    void toggleModel(model)
                  }}
                  aria-pressed={!off}
                >
                  <div className="prov-mline-l">
                    <span className="prov-mline-dot" aria-hidden="true" />
                    <span className="prov-mline-name num">{model}</span>
                    {manual ? <span className="badge badge--muted prov-mline-manual">manual</span> : null}
                  </div>
                  <div className="prov-mline-acts model-toggles" aria-label={`Models for ${provider.id}`}>
                    <span
                      className="prov-mline-acts-inner"
                      onClick={(event) => event.stopPropagation()}
                      onKeyDown={(event) => event.stopPropagation()}
                    >
                    <Toggle
                      checked={!off}
                      onChange={() => void toggleModel(model)}
                      label={`${model} enabled`}
                      visuallyHidden
                    />
                    <button
                      type="button"
                      className="ibtn"
                      onClick={() => {
                        setModalNew(false)
                        setModalModel(model)
                      }}
                      disabled={task.running}
                      aria-label={'Edit ' + model}
                      title={'Edit ' + model}
                    >
                      <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round"><path d="M12 20h9"/><path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4Z"/></svg>
                    </button>
                    <button
                      type="button"
                      className="ibtn ibtn--danger"
                      onClick={() => void removeModel(model)}
                      disabled={task.running}
                      aria-label={`Remove ${model}`}
                      title={`Remove ${model}`}
                    >
                      <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round"><path d="M4 7h16M9 7V4h6v3M6 7l1 13h10l1-13"/></svg>
                    </button>
                    </span>
                  </div>
                </div>
              )
            })}
          </div>
        ) : (
          <p className="meta">No models listed.</p>
        )}
        <div className="prov-addline">
          <input
            className="prov-addinput"
            value={addModel}
            onChange={(event) => setAddModel(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === 'Enter') void addModelSubmit()
            }}
            placeholder="model id, e.g. gpt-5.3"
            spellCheck={false}
          />
          <Button tone="primary" size="sm" onClick={() => void addModelSubmit()} disabled={task.running || addModel.trim() === '' || models.includes(addModel.trim())}>
            Add
          </Button>
          <Button
            tone="ghost"
            size="sm"
            onClick={() => {
              setModalNew(true)
              setModalModel(addModel.trim())
            }}
            disabled={task.running || addModel.trim() === '' || models.includes(addModel.trim())}
          >
            Configure
          </Button>
        </div>
        {modalModel !== null ? (
          <ModelSettingsModal
            providerId={provider.id}
            model={modalModel}
            isNew={modalNew}
            fallbackContextWindow={globalContextWindow}
            initial={modalNew ? undefined : settings[modalModel]}
            busy={task.running}
            onCancel={() => {
              setModalModel(null)
              setModalNew(false)
            }}
            onSave={(draft) => {
              void saveModelSettings(modalModel, draft)
              setModalModel(null)
              setModalNew(false)
            }}
          />
        ) : null}
        <Row gap="tight" align="start">
          <Button tone="ghost" size="sm" onClick={onEdit} disabled={task.running}>
            Edit
          </Button>
          {confirming ? (
            <Confirm
              title={`Delete ${provider.id}?`}
              detail="The provider leaves the config immediately."
              confirmLabel="Delete"
              busy={task.running}
              onCancel={() => setConfirming(false)}
              onConfirm={() => void onDelete()}
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
              <Button tone="ghost" size="sm" onClick={onMutated}>Re-fetch configuration</Button>
            ) : null}
          </Banner>
        ) : null}
        {toggleError !== null ? (
          <Banner tone="error" title="Model toggle failed">
            {describeError(toggleError)}
            <Button tone="ghost" size="sm" onClick={onMutated}>Re-fetch configuration</Button>
          </Banner>
        ) : null}
        {syncError !== null ? (
          <Banner tone="error" title="Model sync failed">
            {describeError(syncError)}
            <Button tone="ghost" size="sm" onClick={onMutated}>Re-fetch configuration</Button>
          </Banner>
        ) : null}
        {modalError !== null ? (
          <Banner tone="error" title="Model settings failed">
            {describeError(modalError)}
            <Button tone="ghost" size="sm" onClick={onMutated}>Re-fetch configuration</Button>
          </Banner>
        ) : null}
      </div>
    </section>
  )
}

export function ProvidersView(): JSX.Element {
  const providers = useAsync<ProvidersViewData>(() => api.providers(), [])
  const [creating, setCreating] = useState(false)
  const [editing, setEditing] = useState<string | null>(null)
  const [selected, setSelected] = useState<string | null>(null)
  const [query, setQuery] = useState('')
  const deleteTask = useTask()

  const list = providers.state.kind === 'ready' ? providers.state.value.providers : []
  function applyOptimistic(id: string, patch: Partial<ProviderView>): void {
    if (providers.state.kind !== 'ready') return
    providers.set({
      ...providers.state.value,
      providers: providers.state.value.providers.map((p) => (p.id === id ? { ...p, ...patch } : p)),
    })
  }
  const activeId = useMemo(() => {
    if (selected !== null && list.some((p) => p.id === selected)) return selected
    return list[0]?.id ?? null
  }, [list, selected])
  const active = activeId === null ? null : list.find((p) => p.id === activeId) ?? null

  async function remove(id: string, generation: number): Promise<void> {
    const ok = await deleteTask.run(() => api.deleteProvider(id, generation))
    if (ok === undefined) return
    setSelected(null)
    providers.refresh()
  }

  async function toggleProvider(provider: ProviderView, generation: number): Promise<void> {
    const next = !(provider.enabled ?? true)
    applyOptimistic(provider.id, { enabled: next })
    try {
      await api.replaceProvider(provider.id, buildWrite(provider, generation, { enabled: next }))
    } catch {
    }
    providers.refresh()
  }

  async function removeModel(provider: ProviderView, model: string, generation: number): Promise<void> {
    applyOptimistic(provider.id, { models: (provider.models ?? []).filter((m) => m !== model) })
    try {
      const nextModels = (provider.models ?? []).filter((m) => m !== model)
      await api.replaceProvider(provider.id, buildWrite(provider, generation, {
        models: nextModels,
        modelSettings: stripRemovedModels(provider.modelSettings, nextModels),
      }))
    } catch {}
    providers.refresh()
  }

  return (
    <section className="screen" aria-labelledby="h-providers">
      <div className="screen-head" style={{ '--i': 0 } as CSSProperties}>
        <div>
          <h1 id="h-providers">
            <svg width="19" height="19" className="h-ic"><use href="#i-plug" /></svg>
            Providers
          </h1>
          <p className="sub">pick a provider on the left, manage its models on the right</p>
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
        <AsyncBoundary<ProvidersViewData>
          state={providers.state}
          loadingLabel="Preparing form…"
          empty={null}
        >
          {(all) => (
            <ProviderEditor
              existing={null}
              generation={all.generation}
              onSaved={(newId) => {
                setCreating(false)
                setSelected(newId)
                providers.refresh()
              }}
              onCancelled={() => setCreating(false)}
            />
          )}
        </AsyncBoundary>
      ) : null}
      <AsyncBoundary<ProvidersViewData>
        state={providers.state}
        loadingLabel="Loading providers…"
        empty={<Empty title="No providers configured." />}
        onRetry={() => providers.refresh()}
      >
        {(all) => {
          const needle = query.trim().toLowerCase()
          const filtered = all.providers.filter(
            (provider) =>
              needle === '' ||
              provider.id.toLowerCase().includes(needle) ||
              provider.wire.toLowerCase().includes(needle) ||
              (provider.models ?? []).some((model) => model.toLowerCase().includes(needle)),
          )
          if (filtered.length === 0) {
            return <Empty title={all.providers.length === 0 ? 'No providers configured.' : 'No providers match the current filter.'} />
          }
          const detail: ReactNode = active === null ? null : editing === active.id ? (
            <div key={`edit-${active.id}`} className="prov-detail-wrap">
              <ProviderEditor
                existing={active}
                generation={all.generation}
                onSaved={(savedId) => {
                  setEditing(null)
                  setSelected(savedId)
                  providers.refresh()
                }}
                onCancelled={() => setEditing(null)}
              />
            </div>
          ) : (
            <div key={`detail-${active.id}`} className="prov-detail-wrap">
              <ProviderDetail
                provider={active}
                generation={all.generation}
                globalContextWindow={all.contextWindow}
                onOptimistic={applyOptimistic}
                onMutated={() => providers.refresh()}
                onEdit={() => setEditing(active.id)}
                onDelete={() => void remove(active.id, all.generation)}
                onToggleProvider={(p) => void toggleProvider(p, all.generation)}
                onRemoveModel={(p, model) => void removeModel(p, model, all.generation)}
              />
            </div>
          )
          return (
            <div className="prov-split">
              <aside className="panel card prov-rail" aria-label="Provider list">
                <div className="prov-rail-label">providers <span className="num">{filtered.length}</span></div>
                {filtered.map((provider) => {
                  const isActive = provider.id === activeId
                  const disabledCount = (provider.disabledModels ?? []).length
                  const credentialTone = provider.credential.state === 'set' ? 'ok' : 'muted'
                  return (
                    <div
                      key={provider.id}
                      className={`prov-prow${isActive ? ' prov-prow--sel' : ''}`}
                      role="button"
                      tabIndex={0}
                      onClick={() => {
                        setSelected(provider.id)
                        setEditing(null)
                      }}
                      onKeyDown={(event) => {
                        if (event.key !== 'Enter' && event.key !== ' ') return
                        event.preventDefault()
                        setSelected(provider.id)
                        setEditing(null)
                      }}
                      aria-current={isActive ? 'true' : undefined}
                    >
                      <div className="prov-prow-top">
                        <span className="prov-prow-name">{provider.id}</span>
                        <Toggle
                          checked={provider.enabled ?? true}
                          onChange={() => void toggleProvider(provider, all.generation)}
                          label={`${provider.id} enabled`}
                          visuallyHidden
                        />
                      </div>
                      <div className="prov-prow-sub">
                        <span className="prov-prow-wire">{provider.wire}</span>
                        <span className={`badge badge--${credentialTone}`}>{provider.credential.state}</span>
                        <span className="num" style={{ marginLeft: 'auto' }}>{(provider.models ?? []).length} models</span>
                      </div>
                      <p className={`meta prov-prow-off${disabledCount > 0 ? '' : ' prov-prow-off--empty'}`}>
                        {disabledCount > 0 ? disabledCount + ' disabled' : 'all models on'}
                      </p>
                    </div>
                  )
                })}
              </aside>
              {detail}
            </div>
          )
        }}
      </AsyncBoundary>
    </section>
  )
}
