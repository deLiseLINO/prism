import { describe, expect, it } from 'vitest'
import type { ConnectPlan } from '@prism/contracts'
import { connectEndpointRows, connectPlanProblem, connectPlanRows } from '../renderer/src/components/ConnectLinkDialog'

function plan(overrides: Partial<ConnectPlan> = {}): ConnectPlan {
  return {
    id: 'edge',
    wire: 'responses',
    baseURL: 'http://10.0.0.1:8080/v1',
    fallbackBaseURLs: ['http://10.0.0.2:8080/v1'],
    endpointMode: 'failover',
    models: ['m1', 'm2'],
    defaultModel: '',
    keyPreview: 'sk-a…',
    hasKey: true,
    exists: false,
    ...overrides,
  }
}

describe('connect plan rows', () => {
  it('marks only the base URL as primary and keeps the fallback order', () => {
    expect(connectEndpointRows(plan({ fallbackBaseURLs: ['http://10.0.0.2:8080/v1', 'https://edge.example/v1'] }))).toEqual([
      { url: 'http://10.0.0.1:8080/v1', primary: true },
      { url: 'http://10.0.0.2:8080/v1', primary: false },
      { url: 'https://edge.example/v1', primary: false },
    ])
  })

  it('lists id, wire, endpoints, mode, models and the masked key', () => {
    expect(connectPlanRows(plan())).toEqual([
      { kind: 'text', label: 'ID', value: 'edge' },
      { kind: 'text', label: 'Wire', value: 'openai-responses' },
      {
        kind: 'endpoints',
        label: 'Endpoints',
        endpoints: [
          { url: 'http://10.0.0.1:8080/v1', primary: true },
          { url: 'http://10.0.0.2:8080/v1', primary: false },
        ],
      },
      { kind: 'text', label: 'Mode', value: 'failover' },
      { kind: 'text', label: 'Models', value: 'm1, m2' },
      { kind: 'text', label: 'Key', value: 'sk-a…' },
    ])
  })

  it('notes model discovery, a default model, spread mode and a missing key', () => {
    const rows = connectPlanRows(plan({
      fallbackBaseURLs: [],
      endpointMode: 'spread',
      models: [],
      defaultModel: 'm1',
      keyPreview: '',
      hasKey: false,
    }))
    expect(rows).toContainEqual({ kind: 'endpoints', label: 'Endpoint', endpoints: [{ url: 'http://10.0.0.1:8080/v1', primary: true }] })
    expect(rows).toContainEqual({ kind: 'text', label: 'Mode', value: 'spread' })
    expect(rows).toContainEqual({ kind: 'text', label: 'Models', value: 'discovered after adding' })
    expect(rows).toContainEqual({ kind: 'text', label: 'Default model', value: 'm1' })
    expect(rows).toContainEqual({ kind: 'text', label: 'Key', value: 'none' })
  })

  it('blocks a plan whose id already exists', () => {
    expect(connectPlanProblem(plan())).toBeNull()
    expect(connectPlanProblem(plan({ exists: true }))).toContain('edge already exists')
  })
})
