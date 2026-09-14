import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { providerWriteFrom, quotaWindows as usageWindows, windowGradient, windowHeader } from '../renderer/src/views/UsageView'
import type { ManagementCall, ManagementReply, ProviderView } from '@prism/contracts'

interface RecordedCall extends ManagementCall {
  readonly timestamp: number
}

const recorded: RecordedCall[] = []

let nextReply: ManagementReply = { ok: true, status: 200, body: {} }

function setReply(reply: ManagementReply): void {
  nextReply = reply
}

const fakeBridge = {
  daemon: {
    status: vi.fn(),
    onStatus: vi.fn(),
  },
  management: {
    call: vi.fn(async (request: ManagementCall): Promise<ManagementReply> => {
      recorded.push({ ...request, timestamp: Date.now() })
      return nextReply
    }),
  },
  integrations: {
    apply: vi.fn(),
    rollback: vi.fn(),
    status: vi.fn(),
  },
  shell: {
    openExternal: vi.fn(),
  },
}

describe('renderer api wrapper', () => {
  beforeEach(() => {
    recorded.length = 0
    nextReply = { ok: true, status: 200, body: {} }
    Object.defineProperty(globalThis, 'window', {
      configurable: true,
      value: { prism: fakeBridge },
      writable: true,
    })
  })

  afterEach(() => {
    vi.resetModules()
  })

  async function loadApi(): Promise<typeof import('../renderer/src/api')> {
    return import('../renderer/src/api')
  }

  it('uses the management bridge seam for providers', async () => {
    const { api } = await loadApi()
    setReply({
      ok: true,
      status: 200,
      body: {
        generation: 7,
        providers: [
          {
            id: 'codex-main',
            wire: 'codex',
            baseURL: '',
            defaultModel: 'gpt-5.2-codex',
            models: ['gpt-5.2-codex'],
            disabledModels: [],
            enabled: true,
            credential: { state: 'unset' },
          },
        ],
      },
    })
    const out = await api.providers()
    expect(out.generation).toBe(7)
    expect(out.providers).toHaveLength(1)
    expect(recorded[0]).toMatchObject({ method: 'GET', path: '/api/v1/providers' })
  })

  it('maps create vs replace to POST vs PUT under the right paths', async () => {
    const { api } = await loadApi()
    setReply({ ok: true, status: 200, body: { generation: 1, providers: [] } })
    await api.createProvider({
      id: 'newprov',
      wire: 'chat',
      models: [],
      disabledModels: [],
      expectedGeneration: 0,
    })
    setReply({ ok: true, status: 200, body: { generation: 2, providers: [] } })
    await api.replaceProvider('newprov', {
      id: 'newprov',
      wire: 'chat',
      models: [],
      disabledModels: [],
      expectedGeneration: 1,
    })
    expect(recorded[0]).toMatchObject({ method: 'POST', path: '/api/v1/providers' })
    expect(recorded[1]).toMatchObject({ method: 'PUT', path: '/api/v1/providers/newprov' })
  })

  it('maps host create and delete to the host routes', async () => {
    const { api } = await loadApi()
    setReply({ ok: true, status: 200, body: { generation: 2, host: { id: 'workmac', local: false, status: 'ok' } } })
    const created = await api.createHost({ id: 'workmac', address: 'user@10.0.0.4', expectedGeneration: 1 })
    expect(created.host.status).toBe('ok')
    expect(recorded[0]).toMatchObject({ method: 'POST', path: '/api/v1/hosts' })
    setReply({ ok: true, status: 200, body: { generation: 3 } })
    await api.deleteHost('workmac', 2)
    expect(recorded[1]).toMatchObject({ method: 'DELETE', path: '/api/v1/hosts/workmac?expectedGeneration=2' })
  })

  it('lists hosts through the same seam', async () => {
    const { api } = await loadApi()
    setReply({ ok: true, status: 200, body: { hosts: [{ id: 'local', local: true, status: 'ok' }] } })
    const view = await api.hosts()
    expect(view.hosts[0]?.id).toBe('local')
    expect(recorded[0]).toMatchObject({ method: 'GET', path: '/api/v1/hosts' })
  })


  it('writes a credential through the same bridge seam without echoing it', async () => {
    const { api } = await loadApi()
    setReply({ ok: true, status: 200, body: { generation: 5, providers: [] } })
    await api.replaceProvider('codex-main', {
      id: 'codex-main',
      wire: 'codex',
      models: [],
      disabledModels: [],
      pool: null,
      credential: 'topsecret-key-9f4e',
      expectedGeneration: 4,
    })
    expect(recorded[0]?.body).toMatchObject({ credential: 'topsecret-key-9f4e' })
    // The bridge was invoked with the secret; the test never echoes it back. The renderer code
    // clears its own local state after the call returns. We just assert the call here.
    expect(recorded).toHaveLength(1)
  })

  it('raises ApiError with the daemon code on conflict', async () => {
    const { api, ApiError } = await loadApi()
    setReply({
      ok: false,
      status: 409,
      body: { error: { code: 'stale_generation', message: 'config moved on' } },
    })
    await expect(api.deleteProvider('codex-main', 3)).rejects.toBeInstanceOf(ApiError)
    await expect(api.deleteProvider('codex-main', 3)).rejects.toMatchObject({
      status: 409,
      code: 'stale_generation',
    })
  })

  it('sends versioned account mutations under their dedicated routes', async () => {
    const { api } = await loadApi()
    setReply({ ok: true, status: 200, body: { id: 'codex:abc', provider: 'codex', state: 'paused' } })
    await api.pauseAccount('codex:abc', 12)
    setReply({ ok: true, status: 200, body: { id: 'codex:abc', provider: 'codex', state: 'active' } })
    await api.resumeAccount('codex:abc', 12)
    setReply({ ok: true, status: 200, body: { id: 'codex:abc', provider: 'codex', state: 'active' } })
    await api.setPriority('codex:abc', 12, 4)
    expect(recorded[0]).toMatchObject({
      method: 'POST',
      path: '/api/v1/accounts/codex%3Aabc/pause',
      body: { version: 12 },
    })
    expect(recorded[1]).toMatchObject({
      method: 'POST',
      path: '/api/v1/accounts/codex%3Aabc/resume',
      body: { version: 12 },
    })
    expect(recorded[2]).toMatchObject({
      method: 'POST',
      path: '/api/v1/accounts/codex%3Aabc/priority',
      body: { version: 12, priority: 4 },
    })
  })

  it('sends DELETE CAS in query parameters and accepts account 204', async () => {
    const { api } = await loadApi()
    setReply({ ok: true, status: 200, body: { generation: 4 } })
    await api.deleteProvider('custom/provider', 3)
    setReply({ ok: true, status: 204 })
    await api.deleteAccount('codex:abc')
    expect(recorded).toEqual([
      expect.objectContaining({ method: 'DELETE', path: '/api/v1/providers/custom%2Fprovider?expectedGeneration=3' }),
      expect.objectContaining({ method: 'DELETE', path: '/api/v1/accounts/codex%3Aabc' }),
    ])
    expect(recorded.every((request) => request.body === undefined)).toBe(true)
  })

  it('drives auth start/status through management without echoing state or codes', async () => {
    const { api } = await loadApi()
    setReply({ ok: true, status: 200, body: { session: 'sess-42', url: 'https://example.com/oauth' } })
    const start = await api.authStart('codex')
    expect(start.session).toBe('sess-42')
    expect(start.url).toBe('https://example.com/oauth')
    setReply({ ok: true, status: 200, body: { provider: 'codex', state: 'pending' } })
    const pending = await api.authStatus('codex', 'sess-42')
    expect(pending.state).toBe('pending')
    setReply({ ok: true, status: 200, body: { provider: 'codex', state: 'authorized' } })
    const ready = await api.authStatus('codex', '')
    expect(ready.state).toBe('authorized')
    expect(recorded[0]).toMatchObject({ method: 'POST', path: '/api/v1/auth/codex/start' })
    expect(recorded[1]).toMatchObject({
      method: 'GET',
      path: '/api/v1/auth/codex/status?session=sess-42',
    })
    expect(recorded[2]).toMatchObject({ method: 'GET', path: '/api/v1/auth/codex/status' })
  })

  it('unwraps provider mutation responses with their hidden pool durations intact', async () => {
    const { api } = await loadApi()
    const pool = {
      strategy: 'quota',
      autoSwitchThreshold: 0.8,
      accountsPath: '',
      maxFailovers: 3,
      cooldownDefault: 300000000000,
      cooldownMax: 900000000000,
      probeEvery: 60000000000,
    }
    setReply({
      ok: true,
      status: 200,
      body: {
        generation: 11,
        provider: {
          id: 'codex-main',
          wire: 'codex',
          defaultModel: 'gpt-5.2-codex',
          models: ['gpt-5.2-codex'],
          disabledModels: [],
          enabled: true,
          pool,
          credential: { state: 'set' },
        },
      },
    })
    const created = await api.createProvider({
      id: 'codex-main',
      wire: 'codex',
      models: [],
      disabledModels: [],
      expectedGeneration: 10,
    })
    expect(created.generation).toBe(11)
    expect(created.provider.id).toBe('codex-main')
    expect(created.provider.pool).toEqual(pool)
    setReply({ ok: true, status: 200, body: { generation: 12, provider: { id: 'codex-main', wire: 'codex', models: [], disabledModels: [], enabled: true, pool } } })
    const replaced = await api.replaceProvider('codex-main', {
      id: 'codex-main',
      wire: 'codex',
      models: [],
      disabledModels: [],
      expectedGeneration: 11,
    })
    expect(replaced.generation).toBe(12)
    expect(replaced.provider.pool).toEqual(pool)
    expect(recorded[0]).toMatchObject({ method: 'POST', path: '/api/v1/providers' })
    expect(recorded[1]).toMatchObject({ method: 'PUT', path: '/api/v1/providers/codex-main' })
  })

  it('unwraps the quota response shape for a single account', async () => {
    const { api } = await loadApi()
    setReply({
      ok: true,
      status: 200,
      body: {
        account: 'codex:abc',
        quota: { used: 42, limit: 120, windowEnd: '2026-08-31T05:00:00Z', source: 'endpoint' },
      },
    })
    const out = await api.quota('codex:abc')
    expect(out).toEqual({
      account: 'codex:abc',
      quota: { used: 42, limit: 120, windowEnd: '2026-08-31T05:00:00Z', source: 'endpoint' },
    })
    expect(recorded[0]).toMatchObject({ method: 'GET', path: '/api/v1/accounts/codex%3Aabc/quota' })
  })

  it('fetches stats with the range in the query string', async () => {
    const { api } = await loadApi()
    const overview = {
      requests: 12,
      completed: 10,
      failed: 2,
      input_tokens: 3400,
      output_tokens: 900,
      cached_tokens: 1000,
      reasoning_tokens: 300,
      total_tokens: 4300,
      measured: 11,
    }
    setReply({
      ok: true,
      status: 200,
      body: {
        range: '7d',
        overview,
        models: [{ model: 'gpt-5.2-codex', provider: 'codex', ...overview }],
        providers: [{ provider: 'codex', ...overview }],
      },
    })
    const out = await api.stats('7d')
    expect(out.range).toBe('7d')
    expect(out.overview.requests).toBe(12)
    expect(out.models[0].provider).toBe('codex')
    expect(out.providers[0].total_tokens).toBe(4300)
    expect(recorded[0]).toMatchObject({
      method: 'GET',
      path: '/api/v1/stats?range=7d',
    })
  })

  it('preserves hidden apiKeyRef and pool durations when editing through the fetch-write seam', async () => {
    const { api } = await loadApi()
    const existingPool = {
      strategy: 'quota',
      autoSwitchThreshold: 0.8,
      accountsPath: '',
      maxFailovers: 3,
      cooldownDefault: 300000000000,
      cooldownMax: 900000000000,
      probeEvery: 60000000000,
    }
    setReply({
      ok: true,
      status: 200,
      body: {
        generation: 10,
        providers: [
          {
            id: 'codex-main',
            wire: 'codex',
            baseURL: 'https://api.example.com',
            defaultModel: 'gpt-5.2-codex',
            models: ['gpt-5.2-codex', 'gpt-5.2'],
            disabledModels: ['gpt-5.2-mini'],
            enabled: true,
            pool: existingPool,
            credential: { state: 'set' },
          },
        ],
      },
    })
    const view = await api.providers()
    const provider = view.providers[0]
    if (provider === undefined) throw new Error('fixture provider missing')
    setReply({ ok: true, status: 200, body: { generation: 11, providers: [] } })
    await api.replaceProvider('codex-main', {
      id: 'codex-main',
      wire: provider.wire,
      baseURL: provider.baseURL ?? null,
      apiKeyRef: null,
      defaultModel: provider.defaultModel ?? null,
      models: provider.models ?? [],
      disabledModels: provider.disabledModels ?? [],
      enabled: provider.enabled ?? true,
      pool: provider.pool ?? null,
      credential: null,
      expectedGeneration: view.generation,
    })
    expect(recorded[1]?.body).toEqual({
      id: 'codex-main',
      wire: 'codex',
      baseURL: 'https://api.example.com',
      apiKeyRef: null,
      defaultModel: 'gpt-5.2-codex',
      models: ['gpt-5.2-codex', 'gpt-5.2'],
      disabledModels: ['gpt-5.2-mini'],
      enabled: true,
      pool: existingPool,
      credential: null,
      expectedGeneration: 10,
    })
  })

  it('writes the vision sidecar through the bridge seam with CAS generation', async () => {
    const { api } = await loadApi()
    setReply({
      ok: true,
      status: 200,
      body: { enabled: true, target: 'router/gpt-5.6-luna', expectedGeneration: 8 },
    })
    const out = await api.visionSidecar({ enabled: true, target: 'router/gpt-5.6-luna', expectedGeneration: 7 })
    expect(out.enabled).toBe(true)
    expect(out.target).toBe('router/gpt-5.6-luna')
    expect(out.expectedGeneration).toBe(8)
    expect(recorded[0]).toMatchObject({
      method: 'PUT',
      path: '/api/v1/vision-sidecar',
      body: { enabled: true, target: 'router/gpt-5.6-luna', expectedGeneration: 7 },
    })
  })

  it('writes the default context window through the bridge seam with CAS generation', async () => {
    const { api } = await loadApi()
    setReply({
      ok: true,
      status: 200,
      body: { contextWindow: 400000, expectedGeneration: 8 },
    })
    const out = await api.contextWindow({ contextWindow: 400000, expectedGeneration: 7 })
    expect(out.contextWindow).toBe(400000)
    expect(out.expectedGeneration).toBe(8)
    expect(recorded[0]).toMatchObject({
      method: 'PUT',
      path: '/api/v1/context-window',
      body: { contextWindow: 400000, expectedGeneration: 7 },
    })
  })

  it('toggles an integration through the bridge seam with CAS generation', async () => {
    const { api } = await loadApi()
    setReply({ ok: true, status: 200, body: { generation: 11, enabled: true } })
    const out = await api.integrationToggle('codex', { enabled: true, expectedGeneration: 10 })
    expect(out).toEqual({ generation: 11, enabled: true })
    expect(recorded[0]).toMatchObject({
      method: 'PUT',
      path: '/api/v1/integrations/codex/enabled',
      body: { enabled: true, expectedGeneration: 10 },
    })
  })

  it('surfaces a stale generation refusal from the toggle as a typed ApiError', async () => {
    const { api } = await loadApi()
    setReply({
      ok: false,
      status: 409,
      body: { error: { code: 'stale_generation', message: 'config moved on' } },
    })
    await expect(api.integrationToggle('grok', { enabled: false, expectedGeneration: 3 })).rejects.toMatchObject({
      status: 409,
      code: 'stale_generation',
    })
  })

  it('returns the widened integrations view with its generation', async () => {
    const { api } = await loadApi()
    const integrations = [
      { id: 'codex', installed: true, managed: true, enabled: true, targetPath: '/c', endpoint: null, drift: false, detail: 'managed by prism' },
    ]
    setReply({ ok: true, status: 200, body: { generation: 9, integrations } })
    const out = await api.integrationsStatus()
    expect(out.generation).toBe(9)
    expect(out.integrations).toEqual(integrations)
    expect(recorded[0]).toMatchObject({ method: 'GET', path: '/api/v1/integrations' })
  })

  it('treats an unexpected 200 reply to account delete as an error, not empty success', async () => {
    const { api, ApiError } = await loadApi()
    setReply({ ok: true, status: 200, body: { account: 'codex:abc' } })
    await expect(api.deleteAccount('codex:abc')).rejects.toBeInstanceOf(ApiError)
  })
})


describe('antigravity usage window labels', () => {
  it('signs both label shapes with family and orders gemini before claude', () => {
    const quota = {
      used: 4000,
      limit: 10000,
      windowEnd: '2026-09-09T00:00:00Z',
      source: 'endpoint' as const,
      windows: [
        { label: 'Claude Weekly', used: 2000, limit: 10000, windowEnd: '2026-09-21T00:00:00Z' },
        { label: 'Gemini 5 hour', used: 4000, limit: 10000, windowEnd: '2026-09-09T00:00:00Z' },
        { label: 'Claude 5 hour', used: 1000, limit: 10000, windowEnd: '2026-09-09T00:00:00Z' },
        { label: 'Weekly Gemini', used: 1250, limit: 10000, windowEnd: '2026-09-21T00:00:00Z' },
      ],
    }
    expect(usageWindows(quota).map((w) => windowHeader(w.label))).toEqual([
      '5h gemini',
      'weekly gemini',
      '5h claude',
      'weekly claude',
    ])
  })

  it('keeps codex window headers unchanged', () => {
    expect(windowHeader('5 hour usage limit')).toBe('5 hour')
    expect(windowHeader('Weekly usage limit')).toBe('Weekly')
  })

  it('colors antigravity bars by family and codex bars by window type', () => {
    const agWindows = usageWindows({
      used: 4000,
      limit: 10000,
      windowEnd: '2026-09-09T00:00:00Z',
      source: 'endpoint',
      windows: [
        { label: 'Gemini 5 hour', used: 4000, limit: 10000, windowEnd: '2026-09-09T00:00:00Z' },
        { label: 'Weekly Gemini', used: 1250, limit: 10000, windowEnd: '2026-09-21T00:00:00Z' },
        { label: 'Claude 5 hour', used: 1000, limit: 10000, windowEnd: '2026-09-09T00:00:00Z' },
        { label: 'Claude Weekly', used: 2000, limit: 10000, windowEnd: '2026-09-21T00:00:00Z' },
      ],
    })
    for (const w of agWindows) {
      expect(windowGradient(w)).toBe(windowHeader(w.label).endsWith('gemini')
        ? 'linear-gradient(90deg, #4285F4, #34A853)'
        : 'linear-gradient(90deg, #6C63FF, #D46DFF)')
    }
    const codexWindows = usageWindows({
      used: 500,
      limit: 10000,
      windowEnd: '2026-09-09T00:00:00Z',
      source: 'endpoint',
      windows: [
        { label: '5 hour usage limit', used: 500, limit: 10000, windowEnd: '2026-09-09T00:00:00Z' },
        { label: 'Weekly usage limit', used: 250, limit: 10000, windowEnd: '2026-09-21T00:00:00Z' },
      ],
    })
    expect(windowGradient(codexWindows[0])).toBe('linear-gradient(90deg, #4285F4, #34A853)')
    expect(windowGradient(codexWindows[1])).toBe('linear-gradient(90deg, #6C63FF, #D46DFF)')
  })
})

describe('antigravity quota windows', () => {
  it('preserves antigravity family windows returned by the API', () => {
    const windows = [
      { label: 'Gemini 5 hour', used: 7500, limit: 10000, windowEnd: '2026-09-10T00:00:00Z' },
      { label: 'Claude 5 hour', used: 6000, limit: 10000, windowEnd: '2026-09-10T00:00:00Z' },
      { label: 'Gemini Weekly', used: 2000, limit: 10000, windowEnd: '2026-09-15T00:00:00Z' },
    ]
    expect(
      usageWindows({
        used: 7500,
        limit: 10000,
        windowEnd: '2026-09-10T00:00:00Z',
        source: 'endpoint',
        windows,
      }).map((w) => w.label),
    ).toEqual(['Gemini 5 hour', 'Gemini Weekly', 'Claude 5 hour'])
  })
})

describe('provider pin write', () => {
  const provider: ProviderView = {
    id: 'ag',
    wire: 'antigravity',
    models: ['gemini-3-pro'],
    disabledModels: [],
    enabled: true,
    pool: {
      strategy: 'quota',
      autoSwitchThreshold: 0.85,
      affinity: 'sticky',
      pinnedAccount: '',
      accountsPath: '',
      maxFailovers: 3,
      cooldownDefault: 300_000_000_000,
      cooldownMax: 3_600_000_000_000,
      probeEvery: 60_000_000_000,
    },
    credential: { state: 'set' },
  }

  it('round-trips the provider and swaps only the pinned account', () => {
    const write = providerWriteFrom(provider, 7, {
      ...(provider.pool as NonNullable<ProviderView['pool']>),
      pinnedAccount: 'antigravity:probe',
    })
    expect(write.id).toBe('ag')
    expect(write.wire).toBe('antigravity')
    expect(write.models).toEqual(['gemini-3-pro'])
    expect(write.expectedGeneration).toBe(7)
    expect(write.pool?.pinnedAccount).toBe('antigravity:probe')
    expect(write.pool?.strategy).toBe('quota')
    expect(write.credential).toBeUndefined()
  })

  it('clears the pin without touching the rest of the pool', () => {
    const pinned = { ...(provider.pool as NonNullable<ProviderView['pool']>), pinnedAccount: 'antigravity:probe' }
    const write = providerWriteFrom({ ...provider, pool: pinned }, 8, {
      ...pinned,
      pinnedAccount: '',
    })
    expect(write.pool?.pinnedAccount).toBe('')
    expect(write.pool?.maxFailovers).toBe(3)
  })
})
