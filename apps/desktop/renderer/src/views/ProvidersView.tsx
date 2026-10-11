import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import type { ModelSettingsView, ProviderView, ProvidersView as ProvidersViewData } from '@prism/contracts'
import { AsyncBoundary, Banner, Button, Confirm, Empty, SearchInput, Toggle } from '../components/Ui'
import { PageHeader, StatusChip } from '../components/Patterns'
import { useAsync, useTask, describeError } from '../useAsync'
import { ApiError, api, type ProviderWrite } from '../api'
import { ModelSettingsModal, draftToSettings, type ModelSettingsDraft } from '../components/ModelSettingsModal'
import { ProviderModal } from '../components/ProviderModal'
import { wireLabel } from '../wires'

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

export function sortProviders(list: readonly ProviderView[], disabledSnapshot: Readonly<Record<string, boolean>>): ProviderView[] {
  return list.slice().sort((a, b) => {
    const oa = disabledSnapshot[a.id] ? 1 : 0
    const ob = disabledSnapshot[b.id] ? 1 : 0
    if (oa !== ob) return oa - ob
    return 0
  })
}

export function enabledModelCount(provider: Pick<ProviderView, 'models' | 'disabledModels'>): number {
  const disabled = new Set(provider.disabledModels ?? [])
  return (provider.models ?? []).filter((model) => !disabled.has(model)).length
}

// The antigravity model view: logical family rows carry effort rungs a user
// set manually in model settings, raw rows are the wire ids the daemon
// routes. The mode switch persists through the daemon, so the stored list,
// the catalog, and agent integrations all move between the two views together.

export function defaultEffort(efforts: readonly string[]): string {
  if (efforts.includes('medium')) return 'medium'
  if (efforts.includes('high')) return 'high'
  return efforts[0] ?? ''
}

export interface RungChip {
  readonly effort: string
  readonly defaultRung: boolean
}

export function rungChips(model: string, settings: Readonly<Record<string, ModelSettingsView>> | undefined): readonly RungChip[] {
  const efforts = settings?.[model]?.reasoningEfforts
  if (efforts === undefined || efforts.length === 0) return []
  const def = defaultEffort(efforts)
  return efforts.map((effort) => ({ effort, defaultRung: effort === def }))
}

export function visibleModelRows(
  models: readonly string[],
  needle: string,
): readonly string[] {
  if (needle === '') return models
  return models.filter((m) => m.toLowerCase().includes(needle))
}

export function clineCatalogModels(models: readonly string[], catalog: 'free' | 'pass'): readonly string[] {
  const prefix = catalog === 'free' ? 'cline-free/' : 'cline-pass/'
  return models.filter((model) => model.startsWith(prefix))
}

export function disabledAfterCatalogToggle(
  disabled: readonly string[],
  visible: readonly string[],
  turnOff: boolean,
): string[] {
  const shown = new Set(visible)
  const kept = disabled.filter((model) => !shown.has(model))
  return turnOff ? [...kept, ...visible] : kept
}

interface DetailProps {
  readonly provider: ProviderView
  readonly generation: number
  readonly globalContextWindow: number
  readonly modelFilter: string
  readonly onOptimistic: (id: string, patch: Partial<ProviderView>) => void
  readonly onMutated: () => void
  readonly onEdit: () => void
  readonly onDelete: () => void
  readonly onToggleProvider: (provider: ProviderView) => void
}
function ProviderDetail({ provider, generation, globalContextWindow, modelFilter, onOptimistic, onMutated, onEdit, onDelete, onToggleProvider }: DetailProps): JSX.Element {
  const [confirming, setConfirming] = useState(false)
  const [addModel, setAddModel] = useState('')
  const [modalModel, setModalModel] = useState<string | null>(null)
  const [modalNew, setModalNew] = useState(false)
  const [modalError, setModalError] = useState<Error | null>(null)
  const [toggleError, setToggleError] = useState<Error | null>(null)
  const [syncError, setSyncError] = useState<Error | null>(null)
  const [syncing, setSyncing] = useState(false)
  const [freshModels, setFreshModels] = useState<readonly string[]>([])
  const [sortDisabled, setSortDisabled] = useState<readonly string[]>(provider.disabledModels ?? [])
  const [catalog, setCatalog] = useState<'free' | 'pass'>('free')
  const task = useTask()
  const models = provider.models ?? []
  const disabledModels = provider.disabledModels ?? []
  const syncedModels = provider.syncedModels ?? []
  const modelSettings = provider.modelSettings
  const rawMode = provider.modelMode === 'raw'
  const hasFamilies = (provider.rawModels ?? []).length > 0 && provider.wire === 'antigravity'

  async function mutate(patch: Partial<ProviderWrite>): Promise<void> {
    const ok = await task.run(() => api.replaceProvider(provider.id, buildWrite(provider, generation, patch)))
    if (ok === undefined) return
    onMutated()
  }

  async function switchMode(mode: 'logical' | 'raw'): Promise<void> {
    try {
      await api.setProviderModelMode(provider.id, mode, generation)
      setToggleError(null)
      setFreshModels([])
    } catch (err: unknown) {
      setToggleError(err instanceof Error ? err : new Error(String(err)))
    }
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
    const scope = provider.wire === 'cline' ? cataloged : models
    const next = disabledAfterCatalogToggle(disabledModels, scope, disabled)
    if (next.length === disabledModels.length && next.every((model, i) => model === disabledModels[i])) return
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
      const before = new Set(models)
      const result = await api.syncProviderModels(provider.id, generation)
      setSortDisabled(result.provider.disabledModels ?? [])
      setFreshModels((result.provider.models ?? []).filter((m) => !before.has(m)))
    } catch (err: unknown) {
      setSyncError(err instanceof Error ? err : new Error(String(err)))
      setFreshModels([])
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
      settings[model] = draftToSettings(draft, provider.wire)
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
    settings[model] = draftToSettings(draft, provider.wire)
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
    const nextModels = models.filter((m) => m !== model)
    onOptimistic(provider.id, { models: nextModels, disabledModels: disabledModels.filter((m) => m !== model) })
    try {
      await api.replaceProvider(provider.id, buildWrite(provider, generation, {
        models: nextModels,
        disabledModels: disabledModels.filter((m) => m !== model),
        modelSettings: stripRemovedModels(provider.modelSettings, nextModels),
      }))
    } catch (err: unknown) {
      setToggleError(err instanceof Error ? err : new Error(String(err)))
    }
    onMutated()
  }

  const settings = provider.modelSettings ?? {}
  const needle = modelFilter.trim().toLowerCase()
  const cataloged = provider.wire === 'cline' ? clineCatalogModels(models, catalog) : models
  const listedModels = visibleModelRows(cataloged, needle)

  const visibleCount = needle === '' ? cataloged.length : listedModels.length
  const allOff = cataloged.length === 0 || cataloged.every((model) => disabledModels.includes(model))
  const allOn = cataloged.length === 0 || cataloged.every((model) => !disabledModels.includes(model))
  const duplicate = addModel.trim() === '' || models.includes(addModel.trim())
  const enabled = provider.enabled ?? true

  return (
    <section className="panel prov-detail">
      <div className="prov-detail__head">
        <div>
          <h2 className="prov-detail__name">
            {provider.id}
            <StatusChip tone={enabled ? 'ok' : 'muted'}>{enabled ? 'Enabled' : 'Disabled'}</StatusChip>
          </h2>
          <p className="prov-detail__wire">
            {wireLabel(provider.wire)}
            {provider.baseURL ? ` · ${provider.baseURL}` : ''}
          </p>
        </div>
        <div className="ctl-row prov-detail__acts">
          <Toggle
            checked={enabled}
            onChange={() => onToggleProvider(provider)}
            label={enabled ? 'Enabled' : 'Disabled'}
            visuallyHidden
          />
          <Button tone="ghost" onClick={onEdit} disabled={task.running}>
            Edit
          </Button>
          <Button tone="danger" onClick={() => setConfirming(true)} disabled={task.running || confirming}>
            Delete
          </Button>
        </div>
      </div>
      {confirming ? (
        <div className="prov-detail__confirm">
          <Confirm
            title={`Delete ${provider.id}?`}
            detail="The provider leaves the config immediately."
            confirmLabel="Delete"
            busy={task.running}
            onCancel={() => setConfirming(false)}
            onConfirm={() => void onDelete()}
          />
        </div>
      ) : null}
      <div className="prov-models__bar ctl-row ctl-row--sm">
        <span>Models</span>
        {provider.wire === 'cline' ? (
          <span className="seg" role="group" aria-label="Cline catalog">
            <button type="button" className="seg-btn" aria-pressed={catalog === 'free'} onClick={() => setCatalog('free')}>free</button>
            <button type="button" className="seg-btn" aria-pressed={catalog === 'pass'} onClick={() => setCatalog('pass')}>pass</button>
          </span>
        ) : null}
        <span className="num">
          {(needle === '' ? cataloged.length : listedModels.length + ' of ' + cataloged.length) +
            (hasFamilies ? (rawMode ? ' raw' : ' logical') : '')}
          {' · '}
          {disabledModels.length} off
        </span>
        <span className="ctl-row ctl-row--sm prov-models__tools">
          {hasFamilies ? (
            <button
              type="button"
              className="btn btn--ghost"
              onClick={() => void switchMode(rawMode ? 'logical' : 'raw')}
              aria-pressed={rawMode}
              aria-label={rawMode ? 'Switch to logical models' : 'Switch to raw models'}
              title={rawMode
                ? 'Switch the provider to the collapsed logical model ids'
                : 'Switch the provider to the raw wire model ids'}
            >
              <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round"><path d="M4 6h16"/><path d="M4 12h10"/><path d="M4 18h13"/></svg>
              <span>{rawMode ? 'logical models' : 'raw models'}</span>
            </button>
          ) : null}
          <button
            type="button"
            className="ibtn"
            onClick={() => void syncModels()}
            disabled={syncing || task.running}
            aria-label="Refresh models from provider"
            title="Refresh models from provider"
          >
            <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" className={syncing ? 'spin' : ''}><path d="M21 12a9 9 0 1 1-2.64-6.36L21 8"/><path d="M21 3v5h-5"/></svg>
          </button>
          <button
            type="button"
            className="ibtn"
            onClick={() => void setAllModelsDisabled(true)}
            disabled={task.running || allOff}
            aria-label="Disable all models"
            title="Disable all"
          >
            <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round"><circle cx="12" cy="12" r="9"/><path d="M5.6 5.6l12.8 12.8"/></svg>
          </button>
          <button
            type="button"
            className="ibtn"
            onClick={() => void setAllModelsDisabled(false)}
            disabled={task.running || allOn}
            aria-label="Enable all models"
            title="Enable all"
          >
            <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round"><path d="M20 6L9 17l-5-5"/></svg>
          </button>
        </span>
      </div>
      {visibleCount > 0 ? (
        <div className="prov-models__list" aria-label={`Models for ${provider.id}`}>
          {listedModels.slice().sort((a, b) => {
            const oa = sortDisabled.includes(a) ? 1 : 0
            const ob = sortDisabled.includes(b) ? 1 : 0
            if (oa !== ob) return oa - ob
            const fa = freshModels.includes(a) ? 0 : 1
            const fb = freshModels.includes(b) ? 0 : 1
            return fa - fb
          }).map((model) => {
            const off = disabledModels.includes(model)
            const manual = syncedModels.length > 0 && !syncedModels.includes(model)
            const fresh = freshModels.includes(model)
            const rungs = rungChips(model, modelSettings)
            const facts = provider.resolvedFacts?.[model]
            const wireTag = facts?.wireSource === 'model' && facts.wire !== undefined ? wireLabel(facts.wire) : null
            return (
              <div
                className={`lrow lrow--click${off ? ' lrow--off' : ''}${fresh ? ' lrow--new' : ''}`}
                key={model}
                role="button"
                tabIndex={0}
                onClick={() => void toggleModel(model)}
                onKeyDown={(event) => {
                  if (event.target !== event.currentTarget) return
                  if (event.key !== 'Enter' && event.key !== ' ') return
                  event.preventDefault()
                  void toggleModel(model)
                }}
                aria-pressed={!off}
              >
                <span className="lrow__lead"><span className="mdot" aria-hidden="true" /></span>
                <div className="lrow__main">
                  <div className="lrow__title">
                    <span className="num" title={model}>{model}</span>
                    {fresh ? <span className="badge badge--ok">new</span> : null}
                    {manual ? <span className="badge badge--muted">manual</span> : null}
                    {wireTag !== null ? <span className="badge badge--muted">{wireTag}</span> : null}
                  </div>
                  {rungs.length > 0 ? (
                    <span className="pm-rungs" aria-label={`Efforts for ${model}`}>
                      {rungs.map((rung) => (
                        <span className={`pm-rung${rung.defaultRung ? ' pm-rung--def' : ''}`} key={rung.effort}>
                          {rung.effort}
                        </span>
                      ))}
                    </span>
                  ) : null}
                </div>
                <div
                  className="lrow__side model-toggles"
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
                    <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round"><path d="M12 20h9"/><path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4Z"/></svg>
                  </button>
                  <button
                    type="button"
                    className="ibtn ibtn--danger"
                    onClick={() => void removeModel(model)}
                    disabled={task.running}
                    aria-label={`Remove ${model}`}
                    title={`Remove ${model}`}
                  >
                    <svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round"><path d="M4 7h16M9 7V4h6v3M6 7l1 13h10l1-13"/></svg>
                  </button>
                </div>
              </div>
            )
          })}
        </div>
      ) : (
        <p className="prov-models__empty">{needle === '' ? 'No models listed. Refresh from the provider or add one below.' : 'No models match the current filter.'}</p>
      )}
      <div className="ctl-row prov-detail__add">
        <input
          className="text-input text-input--mono"
          value={addModel}
          onChange={(event) => setAddModel(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === 'Enter') void addModelSubmit()
          }}
          placeholder="model id, e.g. gpt-5.3"
          aria-label="New model id"
          spellCheck={false}
        />
        <Button tone="primary" onClick={() => void addModelSubmit()} disabled={task.running || duplicate}>
          Add
        </Button>
        <Button
          tone="ghost"
          onClick={() => {
            setModalNew(true)
            setModalModel(addModel.trim())
          }}
          disabled={task.running || duplicate}
        >
          Configure
        </Button>
      </div>
      {modalModel !== null ? (
        <ModelSettingsModal
          providerId={provider.id}
          model={modalModel}
          isNew={modalNew}
          fallbackContextWindow={provider.resolvedFacts?.[modalModel]?.contextWindow ?? globalContextWindow}
          providerWire={provider.wire}
          contextSource={provider.resolvedFacts?.[modalModel]?.contextSource ?? 'global'}
          resolvedImage={provider.resolvedImage?.[modalModel] === true}
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
      <div className="prov-detail__notes">
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
  // Ordering snapshot mirrors the model list: taken when the rail first gets
  // data, never on toggles. Toggling a provider only flips its switch; the
  // order updates when the view next mounts (tab switch).
  const [sortDisabled, setSortDisabled] = useState<Readonly<Record<string, boolean>>>({})
  const snapshotTakenRef = useRef(false)
  const takeSortSnapshot = useCallback((list: readonly ProviderView[]): void => {
    const next: Record<string, boolean> = {}
    for (const provider of list) next[provider.id] = !(provider.enabled ?? true)
    setSortDisabled(next)
  }, [])
  const list = providers.state.kind === 'ready' ? providers.state.value.providers : []
  useEffect(() => {
    if (providers.state.kind !== 'ready' || snapshotTakenRef.current) return
    snapshotTakenRef.current = true
    takeSortSnapshot(providers.state.value.providers)
  }, [providers.state, takeSortSnapshot])

  function applyOptimistic(id: string, patch: Partial<ProviderView>): void {
    if (providers.state.kind !== 'ready') return
    providers.set({
      ...providers.state.value,
      providers: providers.state.value.providers.map((p) => (p.id === id ? { ...p, ...patch } : p)),
    })
  }
  const needle = query.trim().toLowerCase()
  const filtered = useMemo(
    () =>
      list.filter(
        (provider) =>
          needle === '' ||
          provider.id.toLowerCase().includes(needle) ||
          wireLabel(provider.wire).toLowerCase().includes(needle) ||
          (provider.models ?? []).some((model) => model.toLowerCase().includes(needle)),
      ),
    [list, needle],
  )
  const activeId = useMemo(() => {
    if (selected !== null && filtered.some((p) => p.id === selected)) return selected
    return filtered[0]?.id ?? null
  }, [filtered, selected])
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

  return (
    <section className="screen" aria-labelledby="h-providers">
      <PageHeader
        id="h-providers"
        icon="#i-plug"
        title="Providers"
        actions={
          <Button tone="primary" onClick={() => setCreating(true)}>
            New provider
          </Button>
        }
      />
      <AsyncBoundary<ProvidersViewData>
        state={providers.state}
        loadingLabel="Loading providers…"
        empty={<Empty icon="#i-plug" title="No providers configured." />}
        onRetry={() => providers.refresh()}
      >
        {(all) => {
          const detail: ReactNode = active === null ? (
            <Empty icon="#i-plug" title="No provider matches the current filter.">Clear the search to see every provider.</Empty>
          ) : (
            <div key={`detail-${active.id}`} className="prov-detail-wrap">
              <ProviderDetail
                provider={active}
                generation={all.generation}
                globalContextWindow={all.contextWindow}
                modelFilter={query}
                onOptimistic={applyOptimistic}
                onMutated={() => providers.refresh()}
                onEdit={() => setEditing(active.id)}
                onDelete={() => void remove(active.id, all.generation)}
                onToggleProvider={(p) => void toggleProvider(p, all.generation)}
              />
            </div>
          )
          if (all.providers.length === 0) {
            return (
              <Empty
                icon="#i-plug"
                title="No providers configured."
                action={<Button tone="primary" onClick={() => setCreating(true)}>New provider</Button>}
              >
                Add a provider to start routing requests.
              </Empty>
            )
          }
          return (
            <div className="prov-split">
              <aside className="panel prov-rail" aria-label="Provider list">
                <div className="prov-rail__head">
                  <SearchInput
                    id="provider-search"
                    value={query}
                    onChange={setQuery}
                    placeholder="Filter by id, wire, or model"
                    hotkey
                  />
                  <div className="prov-rail__count">
                    <span>providers</span>
                    <span className="num">{filtered.length}</span>
                  </div>
                </div>
                <div className="prov-rail__list">
                  {filtered.length === 0 ? <p className="prov-models__empty">No providers match.</p> : null}
                  {sortProviders(filtered, sortDisabled).map((provider) => {
                    const isActive = provider.id === activeId
                    const on = provider.enabled ?? true
                    return (
                      <div
                        key={provider.id}
                        className={`lrow lrow--click${isActive ? ' lrow--sel' : ''}${on ? '' : ' lrow--off'}`}
                        role="button"
                        tabIndex={0}
                        onClick={() => {
                          setSelected(provider.id)
                          setEditing(null)
                        }}
                        onKeyDown={(event) => {
                          if (event.target !== event.currentTarget) return
                          if (event.key !== 'Enter' && event.key !== ' ') return
                          event.preventDefault()
                          setSelected(provider.id)
                          setEditing(null)
                        }}
                        aria-current={isActive ? 'true' : undefined}
                      >
                        <span className="lrow__lead"><span className={`dot ${on ? 'dot-ok' : 'dot-muted'}`} aria-hidden="true" /></span>
                        <div className="lrow__main">
                          <div className="lrow__title"><span>{provider.id}</span></div>
                          <div className="lrow__sub lrow__sub--mono">
                            {wireLabel(provider.wire)} · {enabledModelCount(provider)} models
                          </div>
                        </div>
                        <div
                          className="lrow__side"
                          onClick={(event) => event.stopPropagation()}
                          onKeyDown={(event) => event.stopPropagation()}
                        >
                          <Toggle
                            checked={on}
                            onChange={() => void toggleProvider(provider, all.generation)}
                            label={`${provider.id} enabled`}
                            visuallyHidden
                          />
                        </div>
                      </div>
                    )
                  })}
                </div>
              </aside>
              {detail}
            </div>
          )
        }}
      </AsyncBoundary>
      {creating || editing !== null ? (
        <AsyncBoundary<ProvidersViewData>
          state={providers.state}
          loadingLabel="Preparing form…"
          empty={null}
        >
          {(all) => {
            const target = editing === null ? null : all.providers.find((p) => p.id === editing) ?? null
            return (
              <ProviderModal
                existing={creating ? null : target}
                generation={all.generation}
                onCancel={() => {
                  setCreating(false)
                  setEditing(null)
                }}
                onSaved={(savedId) => {
                  setCreating(false)
                  setEditing(null)
                  setSelected(savedId)
                  providers.refresh()
                }}
              />
            )
          }}
        </AsyncBoundary>
      ) : null}
    </section>
  )
}
