import { afterEach, describe, expect, it, vi } from 'vitest'
import { applyTargets, rollbackTargets, rowState, setIntegrationEnabled } from '../renderer/src/views/IntegrationsView'
import type { IntegrationStatus } from '@prism/contracts'
import { api, ApiError } from '../renderer/src/api'

vi.mock('../renderer/src/bridge', () => ({
  bridge: {
    agents: { status: vi.fn() },
    management: { call: vi.fn() },
    integrations: { apply: vi.fn(), rollback: vi.fn(), status: vi.fn(), hosts: vi.fn() },
    shell: { openExternal: vi.fn() },
    daemon: { status: vi.fn(), onStatus: vi.fn() },
  },
}))

function status(partial: Partial<IntegrationStatus>): IntegrationStatus {
  return {
    id: 'codex',
    installed: true,
    managed: false,
    enabled: false,
    targetPath: '~/.codex/config.toml',
    endpoint: null,
    drift: false,
    detail: 'installed; no prism-managed bytes present',
    ...partial,
  }
}

describe('integration card row state', () => {
  it('marks uninstalled first, whatever else the detail says', () => {
    expect(rowState(status({ installed: false }))).toBe('uninstalled')
  })

  it('derives damaged from the fence detail', () => {
    expect(rowState(status({
      managed: true,
      detail: 'prism: managed fence is damaged (orphaned markers); apply refuses to guess the block extent',
    }))).toBe('damaged')
  })

  it('puts drift above managed', () => {
    expect(rowState(status({ managed: true, drift: true, endpoint: 'http://127.0.0.1:1/v1', detail: 'managed endpoint drifted' }))).toBe('drift')
  })

  it('splits managed from unmanaged on installed clients', () => {
    expect(rowState(status({ managed: true, detail: 'managed by prism' }))).toBe('managed')
    expect(rowState(status({ managed: false }))).toBe('unmanaged')
  })

  it('does not mistake an ordinary fence mention for damage', () => {
    expect(rowState(status({ managed: true, detail: 'managed by prism; fence intact' }))).toBe('managed')
  })
})

describe('bulk targets', () => {
  it('apply targets installed non-managed cards and skips damaged and uninstalled ones', () => {
    const list = [
      status({ id: 'codex', managed: false }),
      status({ id: 'grok', managed: true, detail: 'managed by prism' }),
      status({ id: 'omp', managed: true, detail: 'prism: managed fence is damaged (orphaned markers)' }),
      status({ id: 'claude', installed: false }),
      status({ id: 'pi', managed: true, drift: true, endpoint: 'http://127.0.0.1:1/v1', detail: 'managed endpoint drifted' }),
    ]
    expect(applyTargets(list).map((s) => s.id)).toEqual(['codex', 'pi'])
  })

  it('rollback targets managed cards and skips damaged and unmanaged ones', () => {
    const list = [
      status({ id: 'codex', managed: false }),
      status({ id: 'grok', managed: true, detail: 'managed by prism' }),
      status({ id: 'omp', managed: true, detail: 'prism: managed fence is damaged (orphaned markers)' }),
      status({ id: 'pi', managed: true, drift: true, endpoint: 'http://127.0.0.1:1/v1', detail: 'managed endpoint drifted' }),
      status({ id: 'claude', installed: false, managed: true }),
    ]
    expect(rollbackTargets(list).map((s) => s.id)).toEqual(['grok', 'pi'])
  })

  it('empty lists disable both bulk buttons', () => {
    expect(applyTargets([status({ id: 'codex', managed: true, detail: 'managed by prism' })])).toEqual([])
    expect(rollbackTargets([status({ id: 'codex', managed: false })])).toEqual([])
  })
})

describe('integration enable toggle flow', () => {
  const write = { enabled: true, expectedGeneration: 10 }

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('persists the toggle and leaves the refresh to the caller on a clean write', async () => {
    const toggle = vi.spyOn(api, 'integrationToggle').mockResolvedValue({ generation: 11, enabled: true })
    const notify = vi.fn()
    const out = await setIntegrationEnabled('codex', true, 10, notify)
    expect(out).toEqual({ generation: 11, enabled: true })
    expect(toggle).toHaveBeenCalledWith('codex', write)
    expect(notify).not.toHaveBeenCalled()
  })

  it('refreshes and retries once with the fresh generation when the token went stale', async () => {
    const toggle = vi
      .spyOn(api, 'integrationToggle')
      .mockRejectedValueOnce(new ApiError(409, 'stale_generation', 'config moved on'))
      .mockResolvedValueOnce({ generation: 12, enabled: true })
    const list = vi.spyOn(api, 'integrationsStatus').mockResolvedValue({ generation: 12, integrations: [] })
    const notify = vi.fn()
    const out = await setIntegrationEnabled('grok', true, 10, notify)
    expect(out).toEqual({ generation: 12, enabled: true })
    expect(toggle).toHaveBeenNthCalledWith(2, 'grok', { enabled: true, expectedGeneration: 12 })
    expect(list).toHaveBeenCalledTimes(1)
    expect(notify).toHaveBeenCalledTimes(1)
  })

  it('propagates a non-stale refusal without a refresh or a retry', async () => {
    const toggle = vi
      .spyOn(api, 'integrationToggle')
      .mockRejectedValue(new ApiError(404, 'not_found', 'unknown integration client'))
    const list = vi.spyOn(api, 'integrationsStatus')
    const notify = vi.fn()
    await expect(setIntegrationEnabled('codex', false, 10, notify)).rejects.toMatchObject({ code: 'not_found' })
    expect(toggle).toHaveBeenCalledTimes(1)
    expect(list).not.toHaveBeenCalled()
    expect(notify).not.toHaveBeenCalled()
  })

  it('gives up after one stale retry when the second write is stale too', async () => {
    const stale = new ApiError(409, 'stale_generation', 'config moved on')
    const toggle = vi.spyOn(api, 'integrationToggle').mockRejectedValue(stale)
    const list = vi.spyOn(api, 'integrationsStatus').mockResolvedValue({ generation: 13, integrations: [] })
    const notify = vi.fn()
    await expect(setIntegrationEnabled('omp', true, 10, notify)).rejects.toMatchObject({ code: 'stale_generation' })
    expect(toggle).toHaveBeenCalledTimes(2)
    expect(list).toHaveBeenCalledTimes(1)
  })
})
