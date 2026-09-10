import { describe, expect, it, vi } from 'vitest'
import type { ManagementCall, ManagementReply } from '@prism/contracts'
import { IntegrationApi, parseIntegrationRequest } from '../shared/integrations'
import type { ManagementProxy } from '../shared/management'

function fakeProxy(reply: ManagementReply): ManagementProxy & { calls: ManagementCall[] } {
  const calls: ManagementCall[] = []
  const proxy = {
    calls,
    call: vi.fn(async (request: ManagementCall) => {
      calls.push(request)
      return reply
    }),
  }
  return proxy as unknown as ManagementProxy & { calls: ManagementCall[] }
}

describe('integration request parsing', () => {
  it('accepts only the three known ids and rejects everything else', () => {
    expect(parseIntegrationRequest({ id: 'grok' })).toEqual({ id: 'grok' })
    expect(parseIntegrationRequest({ id: 'codex', force: true })).toEqual({ id: 'codex', force: true })
    expect(() => parseIntegrationRequest({ id: 'disable' })).toThrow('unknown integration id')
    expect(() => parseIntegrationRequest('codex')).toThrow('must be an object')
    expect(() => parseIntegrationRequest({ id: 'codex', force: 'yes' })).toThrow('force must be a boolean')
  })
})

describe('integration api over the management seam', () => {
  it('maps apply to the management route and returns the daemon result', async () => {
    const proxy = fakeProxy({ ok: true, status: 200, body: { ok: true, id: 'codex' } })
    const api = new IntegrationApi(proxy)
    const result = await api.apply({ id: 'codex' })
    expect(result).toEqual({ ok: true, id: 'codex' })
    expect(proxy.calls).toEqual([{ method: 'POST', path: '/api/v1/integrations/codex/apply' }])
  })

  it('maps rollback to the management route and keeps refusals honest', async () => {
    const refusal = { ok: false, id: 'grok', reason: 'prism: grok is not managed by prism' }
    const proxy = fakeProxy({ ok: true, status: 200, body: refusal })
    const api = new IntegrationApi(proxy)
    expect(await api.rollback({ id: 'grok' })).toEqual(refusal)
    expect(proxy.calls).toEqual([{ method: 'POST', path: '/api/v1/integrations/grok/rollback' }])
  })

  it('sends force as a query flag and keeps plain apply unflagged', async () => {
    const proxy = fakeProxy({ ok: true, status: 200, body: { ok: true, id: 'claude' } })
    const api = new IntegrationApi(proxy)
    await api.apply({ id: 'claude', force: true })
    await api.apply({ id: 'codex' })
    expect(proxy.calls).toEqual([
      { method: 'POST', path: '/api/v1/integrations/claude/apply?force=true' },
      { method: 'POST', path: '/api/v1/integrations/codex/apply' },
    ])
  })

  it('surfaces transport failures as failed results with the daemon reason', async () => {
    const proxy = fakeProxy({ ok: false, status: 0, error: 'connect ECONNREFUSED' })
    const api = new IntegrationApi(proxy)
    const result = await api.apply({ id: 'omp' })
    expect(result.ok).toBe(false)
    expect(result.id).toBe('omp')
    expect(result.reason).toContain('ECONNREFUSED')
  })

  it('maps daemon error bodies into failed results', async () => {
    const proxy = fakeProxy({
      ok: false,
      status: 404,
      body: { error: { code: 'not_found', message: 'unknown integration client codexx' } },
    })
    const api = new IntegrationApi(proxy)
    const result = await api.rollback({ id: 'codex' })
    expect(result.ok).toBe(false)
    expect(result.reason).toContain('unknown integration client')
  })

  it('rejects instead of masking a transport failure as an empty status list', async () => {
    const proxy = fakeProxy({ ok: false, status: 0, error: 'connect ECONNREFUSED' })
    const api = new IntegrationApi(proxy)
    await expect(api.status()).rejects.toThrow('ECONNREFUSED')
  })

  it('rejects an answered non-200 status instead of masking it as empty', async () => {
    const proxy = fakeProxy({
      ok: false,
      status: 503,
      body: { error: { code: 'unavailable', message: 'prism daemon overloaded' } },
    })
    const api = new IntegrationApi(proxy)
    await expect(api.status()).rejects.toThrow('prism daemon overloaded')
  })

  it('rejects a malformed 200 body instead of returning no integrations', async () => {
    const proxy = fakeProxy({ ok: true, status: 200, body: {} })
    const api = new IntegrationApi(proxy)
    await expect(api.status()).rejects.toThrow()
  })

  it('sources statuses from the list route', async () => {
    const statuses = [
      { id: 'codex', installed: true, managed: false, targetPath: '/c', endpoint: null, drift: false, detail: 'installed; no prism-managed bytes present' },
    ]
    const proxy = fakeProxy({ ok: true, status: 200, body: { integrations: statuses } })
    const api = new IntegrationApi(proxy)
    expect(await api.status()).toEqual(statuses)
    expect(proxy.calls).toEqual([{ method: 'GET', path: '/api/v1/integrations' }])
  })

})
