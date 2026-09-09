import { describe, expect, it } from 'vitest'
import { validateManagementCall } from '../main/management'

describe('management path validation', () => {
  it('accepts auth sessions and generation CAS query parameters', () => {
    expect(
      validateManagementCall({
        method: 'GET',
        path: '/api/v1/auth/codex/status?session=session-123',
      }),
    ).toMatchObject({ path: '/api/v1/auth/codex/status?session=session-123' })
    expect(
      validateManagementCall({
        method: 'DELETE',
        path: '/api/v1/providers/custom?expectedGeneration=7',
      }),
    ).toMatchObject({ path: '/api/v1/providers/custom?expectedGeneration=7' })
    expect(
      validateManagementCall({
        method: 'POST',
        path: '/api/v1/integrations/codex/apply?force=true',
      }),
    ).toMatchObject({ path: '/api/v1/integrations/codex/apply?force=true' })
    expect(
      validateManagementCall({
        method: 'GET',
        path: '/api/v1/stats?range=7d',
      }),
    ).toMatchObject({ path: '/api/v1/stats?range=7d' })
  })

  it('rejects traversal, fragments, unknown queries, and DELETE bodies', () => {
    const invalid = [
      '/api/v1/../../secret',
      '/api/v1/%2e%2e/secret',
      '/api/v1/providers#fragment',
      '/api/v1/providers?redirect=https%3A%2F%2Fevil.example',
      '/api/v1/providers?expectedGeneration=',
    ]
    for (const path of invalid) {
      expect(() => validateManagementCall({ method: 'GET', path })).toThrow(/safe \/api\/v1/)
    }
    expect(() =>
      validateManagementCall({
        method: 'DELETE',
        path: '/api/v1/providers/custom?expectedGeneration=7',
        body: { expectedGeneration: 7 },
      }),
    ).toThrow(/body requires POST or PUT/)
  })
})
