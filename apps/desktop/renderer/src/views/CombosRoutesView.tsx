import { useMemo, useState } from 'react'
import type {
  ComboView,
  CombosView,
  ProvidersView,
  RoutesView,
} from '@prism/contracts'
import { AsyncBoundary, Banner, Button, Card, Confirm, Empty, Field, Row, SearchInput, Select, Stack, TextInput, Toggle } from '../components/Ui'
import { useAsync, useTask, describeError } from '../useAsync'
import { ApiError, api, type ComboWrite } from '../api'

interface TargetDraft {
  readonly provider: string
  readonly model: string
  readonly weight: string
}

const STRATEGY_OPTIONS = [
  { value: 'failover', label: 'failover' },
  { value: 'round_robin', label: 'round_robin' },
] as const


interface ComboEditorProps {
  readonly existing: ComboView | null
  readonly generation: number
  readonly providers: readonly { readonly id: string; readonly models: readonly string[] }[]
  readonly onSaved: () => void
  readonly onCancelled: () => void
}

function ComboEditor({
  existing,
  generation,
  providers,
  onSaved,
  onCancelled,
}: ComboEditorProps): JSX.Element {
  const initialId = existing?.id ?? ''
  const [id, setId] = useState(initialId)
  const initialTargets: TargetDraft[] =
    existing?.targets.map((t) => ({ provider: t.provider, model: t.model, weight: String(t.weight) })) ?? []
  const [targets, setTargets] = useState<TargetDraft[]>(initialTargets)
  const [strategy, setStrategy] = useState<'failover' | 'round_robin'>(existing?.strategy ?? 'failover')
  const [stickyLimit, setStickyLimit] = useState<string>(String(existing?.stickyLimit ?? 5))
  const [alias, setAlias] = useState(existing?.alias ?? '')
  const [nativeAlias, setNativeAlias] = useState(existing?.nativeAlias ?? '')
  const [displayName, setDisplayName] = useState(existing?.displayName ?? '')
  const [imageInput, setImageInput] = useState<boolean>(existing?.imageInput ?? false)
  const task = useTask()

  const idMissing = id.trim() === ''
  const sticky = Number(stickyLimit)
  const stickyInvalid = !Number.isInteger(sticky) || sticky < 0
  const targetsInvalid = targets.some((target) => {
    const weight = Number(target.weight)
    return target.provider === '' || target.model === '' || !Number.isFinite(weight) || weight < 1
  })
  const invalid = idMissing || stickyInvalid || targetsInvalid

  function updateTarget(index: number, patch: Partial<TargetDraft>): void {
    setTargets((current) => current.map((target, i) => (i === index ? { ...target, ...patch } : target)))
  }

  function addTarget(): void {
    if (providers.length === 0) return
    const [first] = providers
    if (first === undefined) return
    setTargets((current) => [...current, { provider: first.id, model: first.models[0] ?? '', weight: '1' }])
  }

  function removeTarget(index: number): void {
    setTargets((current) => current.filter((_, i) => i !== index))
  }

  async function save(): Promise<void> {
    if (invalid) return
    const write: ComboWrite = {
      targets: targets.map((target) => ({
        provider: target.provider,
        model: target.model,
        weight: Number(target.weight),
      })),
      strategy,
      stickyLimit: sticky,
      alias,
      nativeAlias,
      displayName,
      imageInput,
      expectedGeneration: generation,
    }
    const ok = await task.run(() => api.putCombo(id.trim(), write))
    if (ok === undefined) return
    onSaved()
  }

  return (
    <Card title={existing === null ? 'New combo' : `Edit combo ${existing.id}`}>
      <div className="field-grid">
        <Field label="ID" htmlFor="combo-id">
          <TextInput id="combo-id" value={id} onChange={setId} disabled={existing !== null} />
          {idMissing ? (
            <div className="inline-error">
              <span>ID is required.</span>
            </div>
          ) : null}
        </Field>
        <Field label="Strategy" htmlFor="combo-strategy">
          <Select<'failover' | 'round_robin'>
            id="combo-strategy"
            value={strategy}
            onChange={setStrategy}
            options={STRATEGY_OPTIONS}
            disabled={task.running}
          />
        </Field>
        <Field label="Sticky limit" htmlFor="combo-sticky">
          <TextInput
            id="combo-sticky"
            type="number"
            value={stickyLimit}
            onChange={setStickyLimit}
            min={0}
            step={1}
          />
          {stickyInvalid ? (
            <div className="inline-error">
              <span>Sticky limit must be a whole number of 0 or more.</span>
            </div>
          ) : null}
        </Field>
        <Field label="Alias" htmlFor="combo-alias">
          <TextInput id="combo-alias" value={alias} onChange={setAlias} />
        </Field>
        <Field label="Native alias" htmlFor="combo-native">
          <TextInput id="combo-native" value={nativeAlias} onChange={setNativeAlias} />
        </Field>
        <Field label="Display name" htmlFor="combo-display">
          <TextInput id="combo-display" value={displayName} onChange={setDisplayName} />
        </Field>
        <Field label="Image input" htmlFor="combo-image">
          <Toggle checked={imageInput} onChange={setImageInput} label={imageInput ? 'Yes' : 'No'} />
        </Field>
      </div>
      <Card title="Targets" description="Provider/model pairs tried in order.">
        {targets.length === 0 ? (
          <p className="meta">No targets yet.</p>
        ) : (
          <ul className="bare-list">
            {targets.map((target, index) => {
              const providerOptions = providers
                .find((provider) => provider.id === target.provider)?.models ?? []
              return (
                <li key={`${target.provider}-${target.model}-${index}`} className="bare-list__row">
                  <select
                    className="select"
                    value={target.provider}
                    onChange={(event) => {
                      const nextProvider = event.target.value
                      const nextModels = providers.find((provider) => provider.id === nextProvider)?.models ?? []
                      updateTarget(index, { provider: nextProvider, model: nextModels[0] ?? '' })
                    }}
                    aria-label={`Provider for target ${index + 1}`}
                  >
                    {providers.map((provider) => (
                      <option key={provider.id} value={provider.id}>
                        {provider.id}
                      </option>
                    ))}
                  </select>
                  <select
                    className="select"
                    value={target.model}
                    onChange={(event) => updateTarget(index, { model: event.target.value })}
                    aria-label={`Model for target ${index + 1}`}
                  >
                    {target.model === '' ? <option value="">Select a model</option> : null}
                    {providerOptions.map((model) => (
                      <option key={model} value={model}>
                        {model}
                      </option>
                    ))}
                  </select>
                  <TextInput
                    id={`weight-${index}`}
                    type="number"
                    value={target.weight}
                    onChange={(next) => updateTarget(index, { weight: next })}
                    min={1}
                  />
                  <Button tone="danger" onClick={() => removeTarget(index)}>
                    Remove
                  </Button>
                </li>
              )
            })}
          </ul>
        )}
        {targetsInvalid ? (
          <div className="inline-error">
            <span>Every target needs a provider, a model, and a weight of at least 1.</span>
          </div>
        ) : null}
        <Row gap="loose" align="start">
          <Button tone="ghost" onClick={addTarget} disabled={providers.length === 0}>
            Add target
          </Button>
        </Row>
      </Card>
      <Row gap="loose" align="start">
        <Button tone="primary" onClick={() => void save()} disabled={task.running || invalid} busy={task.running}>
          {existing === null ? 'Create combo' : 'Save combo'}
        </Button>
        <Button tone="ghost" onClick={onCancelled} disabled={task.running}>
          Cancel
        </Button>
      </Row>
      {task.error !== null ? (
        <Banner tone="error" title="Combo write failed">
          {describeError(task.error)}
          {task.error instanceof ApiError && task.error.code === 'stale_generation' ? (
            <>
              : the config changed.
              <Button tone="ghost" onClick={onSaved}>Re-fetch configuration</Button>
            </>
          ) : null}
        </Banner>
      ) : null}
    </Card>
  )
}

interface ComboRowProps {
  readonly combo: ComboView
  readonly generation: number
  readonly providers: readonly { readonly id: string; readonly models: readonly string[] }[]
  readonly providersReady: boolean
  readonly onChanged: () => void
}

function ComboRow({ combo, generation, providers, providersReady, onChanged }: ComboRowProps): JSX.Element {
  const [editing, setEditing] = useState(false)
  const [confirming, setConfirming] = useState(false)
  const task = useTask()
  async function remove(): Promise<void> {
    const ok = await task.run(() => api.deleteCombo(combo.id, generation))
    if (ok === undefined) return
    setConfirming(false)
    onChanged()
  }

  if (editing) {
    return (
      <ComboEditor
        existing={combo}
        generation={generation}
        providers={providers}
        onSaved={() => {
          setEditing(false)
          onChanged()
        }}
        onCancelled={() => setEditing(false)}
      />
    )
  }

  return (
    <Card
      title={combo.id}
      description={`${combo.strategy} · sticky ${combo.stickyLimit} · ${combo.targets.length} target(s)`}
      action={
        combo.imageInput === true ? <span className="badge badge--info">image input</span> : null
      }
    >
      <ul className="bare-list">
        {combo.targets.map((target, index) => (
          <li key={`${target.provider}/${target.model}/${target.weight}`}>
            <span className="badge badge--muted">{index + 1}</span>
            <span className="cell-mono">
              {target.provider}/{target.model}
            </span>
            <span className="badge">w {target.weight}</span>
          </li>
        ))}
      </ul>
      {combo.alias !== '' || combo.nativeAlias !== '' || combo.displayName !== '' ? (
        <p className="meta">
          Aliases: {combo.alias !== '' ? combo.alias : '—'} · Native:{' '}
          {combo.nativeAlias !== '' ? combo.nativeAlias : '—'} · Display:{' '}
          {combo.displayName !== '' ? combo.displayName : '—'}
        </p>
      ) : null}
      <Row gap="tight" align="start">
        <Button
          tone="ghost"
          size="sm"
          onClick={() => setEditing(true)}
          disabled={!providersReady}
          title={providersReady ? undefined : 'Providers are still loading.'}
        >
          Edit
        </Button>
        {confirming ? (
          <Confirm
            title={`Delete ${combo.id}?`}
            detail="Routes pointing at this combo keep their key with a missing target."
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
    </Card>
  )
}

interface RouteRowProps {
  readonly keyName: string
  readonly value: string
  readonly generation: number
  readonly onChanged: () => void
}

function RouteRow({ keyName, value, generation, onChanged }: RouteRowProps): JSX.Element {
  const [draft, setDraft] = useState(value)
  const [confirming, setConfirming] = useState(false)
  const task = useTask()
  const valueMissing = draft.trim() === ''
  async function save(): Promise<void> {
    if (valueMissing) return
    const ok = await task.run(() => api.putRoute(keyName, { value: draft, expectedGeneration: generation }))
    if (ok === undefined) return
    onChanged()
  }
  async function remove(): Promise<void> {
    const ok = await task.run(() => api.deleteRoute(keyName, generation))
    if (ok === undefined) return
    setConfirming(false)
    onChanged()
  }
  return (
    <tr>
      <td className="cell-mono">{keyName}</td>
      <td>
        <TextInput id={`route-${keyName.replace(/[^A-Za-z0-9_-]/g, '-')}`} value={draft} onChange={setDraft} />
        {valueMissing ? (
          <div className="inline-error">
            <span>Route value is required.</span>
          </div>
        ) : null}
      </td>
      <td>
        <Row gap="tight" align="end">
          <Button tone="primary" size="sm" onClick={() => void save()} disabled={task.running || valueMissing} busy={task.running}>
            Save
          </Button>
          {confirming ? (
            <Confirm
              title={`Delete route ${keyName}?`}
              detail="Inbound aliases stop resolving to this combo."
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
          <div className="inline-error">
            <span>{describeError(task.error)}</span>
            {task.error instanceof ApiError && task.error.code === 'stale_generation' ? (
              <Button tone="ghost" size="sm" onClick={onChanged}>Re-fetch routes</Button>
            ) : null}
          </div>
        ) : null}
      </td>
    </tr>
  )
}

export function CombosRoutesView(): JSX.Element {
  const combos = useAsync<CombosView>(() => api.combos(), [])
  const routes = useAsync<RoutesView>(() => api.routes(), [])
  const providers = useAsync<ProvidersView>(() => api.providers(), [])
  const [creating, setCreating] = useState(false)
  const [routeKey, setRouteKey] = useState('')
  const [routeValue, setRouteValue] = useState('')
  const [query, setQuery] = useState('')
  const routeTask = useTask()
  const providerModels = useMemo(() => {
    if (providers.state.kind !== 'ready') return []
    return providers.state.value.providers.map((provider) => ({
      id: provider.id,
      models: provider.models ?? [],
    }))
  }, [providers.state])

  function refresh(): void {
    combos.refresh()
    routes.refresh()
    providers.refresh()
  }

  async function createRoute(): Promise<void> {
    const snapshot = routes.state
    if (snapshot.kind !== 'ready' || routeKey.trim() === '' || routeValue.trim() === '') return
    const result = await routeTask.run(() =>
      api.putRoute(routeKey.trim(), {
        value: routeValue.trim(),
        expectedGeneration: snapshot.value.generation,
      }),
    )
    if (result === undefined) return
    setRouteKey('')
    setRouteValue('')
    refresh()
  }

  return (
    <Stack gap="normal">
      <div className="toolbar">
        <div className="toolbar__filters">
          <SearchInput
            id="combo-search"
            value={query}
            onChange={setQuery}
            placeholder="Filter combos or routes"
          />
        </div>
        <div className="toolbar__actions">
          <Button tone="primary" size="sm" onClick={() => setCreating(true)}>
            New combo
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
            <ComboEditor
              existing={null}
              generation={list.generation}
              providers={list.providers.map((provider) => ({ id: provider.id, models: provider.models ?? [] }))}
              onSaved={() => {
                setCreating(false)
                refresh()
              }}
              onCancelled={() => setCreating(false)}
            />
          )}
        </AsyncBoundary>
      ) : null}
      <AsyncBoundary<CombosView>
        state={combos.state}
        loadingLabel="Loading combos…"
        empty={<Empty title="No combos configured." />}
        onRetry={() => combos.refresh()}
      >
        {(list) => {
          const needle = query.trim().toLowerCase()
          const filtered = list.combos.filter(
            (combo) =>
              needle === '' ||
              combo.id.toLowerCase().includes(needle) ||
              combo.targets.some(
                (target) =>
                  target.provider.toLowerCase().includes(needle) ||
                  target.model.toLowerCase().includes(needle),
              ),
          )
          if (filtered.length === 0) {
            return <Empty title={list.combos.length === 0 ? 'No combos configured.' : 'No combos match the current filter.'} />
          }
          return (
            <>
              {filtered.map((combo) => (
                <ComboRow
                  key={combo.id}
                  combo={combo}
                  generation={list.generation}
                  providers={providerModels}
                  providersReady={providers.state.kind === 'ready'}
                  onChanged={refresh}
                />
              ))}
            </>
          )
        }}
      </AsyncBoundary>
      <Card
        title="Routes"
        description="Inbound alias → combo mapping."
        action={
          <p className="meta">
            {routes.state.kind === 'ready'
              ? `${Object.keys(routes.state.value.routes).length} route(s)`
              : '—'}
          </p>
        }
      >
        <div className="field-grid">
          <Field label="Route key" htmlFor="route-key">
            <TextInput id="route-key" value={routeKey} onChange={setRouteKey} placeholder="model alias" />
          </Field>
          <Field label="Combo" htmlFor="route-value">
            <TextInput id="route-value" value={routeValue} onChange={setRouteValue} placeholder="combo id" />
          </Field>
          <Button
            tone="primary"
            size="sm"
            onClick={() => void createRoute()}
            disabled={
              routeTask.running ||
              routes.state.kind !== 'ready' ||
              routeKey.trim() === '' ||
              routeValue.trim() === ''
            }
            busy={routeTask.running}
          >
            Set route
          </Button>
        </div>
        {routes.state.kind !== 'ready' ? (
          <p className="meta">Routes are still loading — setting a route will be possible shortly.</p>
        ) : null}
        {routeTask.error !== null ? (
          <Banner tone="error" title="Route write failed">
            {describeError(routeTask.error)}
            {routeTask.error instanceof ApiError && routeTask.error.code === 'stale_generation' ? (
              <Button tone="ghost" onClick={refresh}>Re-fetch routes</Button>
            ) : null}
          </Banner>
        ) : null}
        <AsyncBoundary<RoutesView>
          state={routes.state}
          loadingLabel="Loading routes…"
          empty={<Empty title="No routes configured." />}
          onRetry={() => routes.refresh()}
        >
          {(list) => {
            const needle = query.trim().toLowerCase()
            const keys = Object.keys(list.routes).filter(
              (keyName) =>
                needle === '' ||
                keyName.toLowerCase().includes(needle) ||
                (list.routes[keyName] ?? '').toLowerCase().includes(needle),
            )
            if (keys.length === 0) {
              return <Empty title={Object.keys(list.routes).length === 0 ? 'No routes configured.' : 'No routes match the current filter.'} />
            }
            return (
              <div className="table-wrap">
                <table className="table">
                  <thead>
                  <tr>
                    <th scope="col">Key</th>
                    <th scope="col">Value</th>
                    <th scope="col">Actions</th>
                  </tr>
                </thead>
                <tbody>
                  {keys.map((keyName) => (
                    <RouteRow
                      key={keyName}
                      keyName={keyName}
                      value={list.routes[keyName] ?? ''}
                      generation={list.generation}
                      onChanged={refresh}
                    />
                  ))}
                </tbody>
                </table>
              </div>
            )
          }}
        </AsyncBoundary>
      </Card>
    </Stack>
  )
}
