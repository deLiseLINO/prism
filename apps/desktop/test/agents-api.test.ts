import { describe, expect, it, vi } from 'vitest'
import type { ManagementCall, ManagementReply } from '@prism/contracts'
import { AgentsApi, parseAgentJobRequest } from '../shared/agents'
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

describe('agent job request parsing', () => {
  it('accepts the eight agent ids and rejects everything else', () => {
    expect(parseAgentJobRequest({ id: 'codex' })).toEqual({ id: 'codex' })
    expect(parseAgentJobRequest({ id: 'hermes', force: true })).toEqual({ id: 'hermes', force: true })
    expect(() => parseAgentJobRequest({ id: 'cursor' })).toThrow('unknown agent id')
    expect(() => parseAgentJobRequest('codex')).toThrow('must be an object')
    expect(() => parseAgentJobRequest({ id: 'codex', force: 'yes' })).toThrow('force must be a boolean')
  })
})

describe('agents api over the management seam', () => {
  const job = { agent: 'codex', op: 'install', state: 'installing', method: 'npm', command: 'npm install -g @openai/codex' }

  it('maps install to the management route with the force query', async () => {
    const proxy = fakeProxy({ ok: true, status: 202, body: { job } })
    const api = new AgentsApi(proxy)
    const result = await api.install({ id: 'codex' })
    expect(result).toEqual({ ok: true, job })
    await api.install({ id: 'claude', force: true })
    expect(proxy.calls).toEqual([
      { method: 'POST', path: '/api/v1/agents/codex/install' },
      { method: 'POST', path: '/api/v1/agents/claude/install?force=true' },
    ])
  })

  it('maps update to the management route and accepts both 200 and 202', async () => {
    const accepted = fakeProxy({ ok: true, status: 200, body: { job: { ...job, op: 'update' } } })
    await new AgentsApi(accepted).update({ id: 'grok' })
    expect(accepted.calls).toEqual([{ method: 'POST', path: '/api/v1/agents/grok/update' }])

    const alsoAccepted = fakeProxy({ ok: true, status: 202, body: { job } })
    const reply = await new AgentsApi(alsoAccepted).update({ id: 'grok' })
    expect(reply.ok).toBe(true)
  })

  it('surfaces the install_active conflict as a failed reply with the daemon reason', async () => {
    const proxy = fakeProxy({ ok: true, status: 409, body: { error: { code: 'install_active', message: 'already active' } } })
    const result = await new AgentsApi(proxy).install({ id: 'omp' })
    expect(result).toEqual({ ok: false, id: 'omp', reason: 'already active' })
  })

  it('surfaces transport failures with the error detail', async () => {
    const proxy = fakeProxy({ ok: false, status: 0, error: 'connect ECONNREFUSED' })
    const result = await new AgentsApi(proxy).update({ id: 'pi' })
    expect(result).toEqual({ ok: false, id: 'pi', reason: 'connect ECONNREFUSED' })
  })

  it('reads the job envelope and throws on malformed bodies', async () => {
    const proxy = fakeProxy({ ok: true, status: 200, body: { job: { agent: 'codex', op: '', state: 'idle' } } })
    const api = new AgentsApi(proxy)
    expect(await api.job({ id: 'codex' })).toEqual({ agent: 'codex', op: '', state: 'idle' })

    const broken = fakeProxy({ ok: true, status: 200, body: { nope: true } })
    await expect(new AgentsApi(broken).job({ id: 'codex' })).rejects.toThrow('malformed')
  })

  it('lists agent statuses from the collection route', async () => {
    const agents = [{ id: 'codex', key: 'codex', installed: true, source: 'npm', canUpdate: true, job: { agent: 'codex', op: '', state: 'idle' } }]
    const proxy = fakeProxy({ ok: true, status: 200, body: { agents } })
    const api = new AgentsApi(proxy)
    expect(await api.status()).toEqual(agents)
    expect(proxy.calls).toEqual([{ method: 'GET', path: '/api/v1/agents' }])

    const broken = fakeProxy({ ok: true, status: 200, body: { integrations: [] } })
    await expect(new AgentsApi(broken).status()).rejects.toThrow('malformed')
  })
})
