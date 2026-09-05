import { useMemo, useState } from 'react'
import type { ModelView } from '@prism/contracts'
import { AsyncBoundary, Card, Empty, Field, SearchInput, Select, Stack } from '../components/Ui'
import { useAsync } from '../useAsync'
import { api } from '../api'
interface ModelsResponse {
  readonly models: readonly ModelView[]
}

type Capability = keyof ModelView['caps']
type CapabilityFilter = Capability | 'all'

const CAPABILITIES = [
  { key: 'reasoning', label: 'Reasoning' },
  { key: 'customTools', label: 'Custom tools' },
  { key: 'localShell', label: 'Local shell' },
  { key: 'toolSearch', label: 'Tool search' },
  { key: 'compaction', label: 'Compaction' },
  { key: 'countTokens', label: 'Count tokens' },
  { key: 'parallelTools', label: 'Parallel tools' },
] satisfies readonly { readonly key: Capability; readonly label: string }[]

const CAPABILITY_OPTIONS = [
  { value: 'all', label: 'All capabilities' },
  ...CAPABILITIES.map(({ key, label }) => ({ value: key, label })),
] satisfies readonly { readonly value: CapabilityFilter; readonly label: string }[]

function activeCapabilities(model: ModelView): readonly string[] {
  return CAPABILITIES.filter(({ key }) => model.caps[key]).map(({ label }) => label)
}

export function ModelsView(): JSX.Element {
  const models = useAsync<ModelsResponse>(() => api.models(), [])
  const [query, setQuery] = useState('')
  const [capability, setCapability] = useState<CapabilityFilter>('all')

  const filtered = useMemo(() => {
    if (models.state.kind !== 'ready') return []
    const needle = query.trim().toLowerCase()
    return models.state.value.models.filter((model) => {
      const matchesText = needle === '' || model.id.toLowerCase().includes(needle) || model.alias?.toLowerCase().includes(needle)
      const matchesCapability = capability === 'all' || model.caps[capability]
      return matchesText && matchesCapability
    })
  }, [capability, models.state, query])

  return (
    <Stack gap="normal">
      <AsyncBoundary<ModelsResponse>
        state={models.state}
        loadingLabel="Loading catalog…"
        empty={<Empty title="No models reported." />}
        onRetry={() => models.refresh()}
      >
        {(list) => (
          <Card
            title="Inbound catalog"
            description="Models served by /v1/models."
            action={
              <p className="meta">{filtered.length} of {list.models.length} model(s)</p>
            }
          >
            <div className="toolbar">
              <div className="toolbar__filters">
                <Field label="Search" htmlFor="model-search">
                  <SearchInput id="model-search" value={query} onChange={setQuery} placeholder="ID or alias" />
                </Field>
                <Field label="Capability" htmlFor="model-capability">
                  <Select<CapabilityFilter>
                    id="model-capability"
                    value={capability}
                    onChange={setCapability}
                    options={CAPABILITY_OPTIONS}
                  />
                </Field>
              </div>
            </div>
            {filtered.length === 0 ? (
              <Empty title="No models match the current filters." />
            ) : (
              <div className="table-wrap">
                <table className="table">
                  <thead>
                    <tr>
                      <th scope="col">ID</th>
                      <th scope="col">Alias</th>
                      <th scope="col">Capabilities</th>
                    </tr>
                  </thead>
                  <tbody>
                    {filtered.map((model) => {
                      const capabilities = activeCapabilities(model)
                      return (
                        <tr key={model.id}>
                          <td className="cell-mono">{model.id}</td>
                          <td>{model.alias ?? '—'}</td>
                          <td>
                            <div className="caps-list">
                              {capabilities.length === 0 ? (
                                <span className="meta">Standard</span>
                              ) : (
                                capabilities.map((label) => (
                                  <span className="badge badge--muted" key={label}>{label}</span>
                                ))
                              )}
                            </div>
                          </td>
                        </tr>
                      )
                    })}
                  </tbody>
                </table>
              </div>
            )}
          </Card>
        )}
      </AsyncBoundary>
    </Stack>
  )
}
