import { describe, expect, it } from 'vitest'
import { previewFromOutcome, renameDraftProblem } from '../renderer/src/components/ProviderRename'

describe('provider id rename in the editor', () => {
  it('flags ids outside the provider id grammar and ignores the unchanged id', () => {
    expect(renameDraftProblem('gw', 'gw')).toBeNull()
    expect(renameDraftProblem('gw', '')).toBeNull()
    expect(renameDraftProblem('gw', 'edge_1')).toBeNull()
    expect(renameDraftProblem('gw', 'ed.ge')).not.toBeNull()
    expect(renameDraftProblem('gw', 'ed/ge')).not.toBeNull()
    expect(renameDraftProblem('gw', '__')).not.toBeNull()
  })

  it('keeps the plan of a clean dry run and every conflict of a refused one', () => {
    const plan = { configRefs: 3, accounts: ['gw:default'], usageAttempts: 5 }
    expect(previewFromOutcome('edge', { kind: 'ok', response: { generation: 4, from: 'gw', to: 'edge', dryRun: true, plan } })).toEqual({
      kind: 'ready',
      to: 'edge',
      response: { generation: 4, from: 'gw', to: 'edge', dryRun: true, plan },
    })
    const conflicts = [
      { kind: 'provider_exists' as const, detail: 'provider "router" already exists' },
      { kind: 'model_shadowed' as const, detail: 'route key "router/x" already claims a model id of "router"' },
    ]
    expect(previewFromOutcome('router', { kind: 'blocked', conflicts })).toEqual({ kind: 'blocked', to: 'router', conflicts })
  })
})
