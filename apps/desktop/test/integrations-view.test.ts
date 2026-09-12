import { describe, expect, it } from 'vitest'
import { applyTargets, rollbackTargets, rowState } from '../renderer/src/views/IntegrationsView'
import type { IntegrationStatus } from '@prism/contracts'

function status(partial: Partial<IntegrationStatus>): IntegrationStatus {
  return {
    id: 'codex',
    installed: true,
    managed: false,
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
