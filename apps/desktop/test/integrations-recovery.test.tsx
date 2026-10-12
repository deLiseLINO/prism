// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { IntegrationApplyResult, IntegrationId, IntegrationStatus } from '@prism/contracts'
import { IntegrationsView } from '../renderer/src/views/IntegrationsView'
import { ExperimentalFlagsProvider } from '../renderer/src/experimental'
import { api } from '../renderer/src/api'

vi.mock('../renderer/src/bridge', () => ({
  bridge: { agents: { status: async () => ({ agents: [] }) } },
}))

let root: Root
let element: HTMLDivElement
let rows: IntegrationStatus[]

function status(partial: Partial<IntegrationStatus> = {}): IntegrationStatus {
  return {
    id: 'omp', installed: true, managed: true, enabled: true,
    targetPath: '/home/u/config/settings.json', endpoint: null, drift: true,
    conflict: 'journal', detail: 'recovery metadata has unsupported version 9',
    ...partial,
  }
}

beforeEach(() => {
  Reflect.set(globalThis, 'IS_REACT_ACT_ENVIRONMENT', true)
  window.localStorage.setItem('prism-experimental', JSON.stringify({ otherAgents: true }))
  element = document.createElement('div')
  document.body.appendChild(element)
  root = createRoot(element)
  rows = [status()]
  vi.spyOn(api, 'integrationsStatus').mockImplementation(async () => ({ generation: 1, integrations: rows }))
})

afterEach(() => {
  act(() => root.unmount())
  element.remove()
  window.localStorage.clear()
  vi.restoreAllMocks()
})

async function mount(): Promise<void> {
  await act(async () => {
    root.render(<ExperimentalFlagsProvider><IntegrationsView /></ExperimentalFlagsProvider>)
  })
}

function card(): HTMLElement {
  const node = element.querySelector<HTMLElement>('.int-item')
  if (node === null) throw new Error('Missing integration card')
  return node
}

function button(label: string, scope: ParentNode = card()): HTMLButtonElement {
  const node = Array.from(scope.querySelectorAll('button')).find((candidate) => candidate.textContent?.trim() === label)
  if (node === undefined) throw new Error(`Missing button ${label}`)
  return node
}

async function click(label: string, scope: ParentNode = card()): Promise<void> {
  await act(async () => button(label, scope).click())
}

function backupPaths(): string[] {
  return Array.from(card().querySelectorAll('.lrow-note .more dd')).map((node) => node.textContent ?? '')
}

const clients: readonly IntegrationId[] = ['codex', 'grok', 'omp', 'claude', 'pi', 'opencode', 'hermes']

describe('confirmed integration recovery', () => {
  it.each(clients)('opens %s recovery directly, with the reported reason and client-specific consequences', async (id) => {
    rows = [status({ id })]
    const apply = vi.spyOn(api, 'integrationApply')
    await mount()
    expect(card().textContent).toContain('Needs repair')
    expect(card().textContent).toContain(rows[0]?.detail)
    expect(element.querySelector('.int-stats')?.textContent).toContain('1needs repair')
    await click('Apply')
    expect(apply).not.toHaveBeenCalled()
    const dialog = card().querySelector('[role=alertdialog]')
    expect(dialog?.textContent).toContain(id === 'claude' ? 'Prism environment fields' : id === 'codex' ? 'Prism routing fields and managed fence' : 'Prism provider block')
    expect(dialog?.textContent).toContain('pre-reconnect state')
    expect(dialog?.textContent).toContain('not automatically restorable')
    expect(dialog?.textContent).toContain('separate copy')
    expect(dialog?.textContent).not.toContain('.prism-journal.json')
    expect(card().querySelector('.lrow-note')?.textContent).toContain(rows[0]?.detail)
    await click('Cancel')
    await click('Review')
    expect(card().querySelector('[role=alertdialog]')).not.toBeNull()
    expect(apply).not.toHaveBeenCalled()
  })

  it.each([true, false])('describes auto-apply honestly when enabled=%s', async (enabled) => {
    rows = [status({ enabled })]
    await mount()
    expect(card().textContent).toContain(enabled ? 'Auto-apply is paused' : 'Auto-apply is off. This still needs repair')
    if (!enabled) expect(card().textContent).not.toContain('paused')
    expect(card().textContent).not.toContain('last background apply')
    expect(card().textContent).not.toContain('changed outside Prism')
  })

  it.each(clients)('shows %s non-forceable config errors without a recovery action', async (id) => {
    rows = [status({ id, blocked: true, detail: 'cannot read config: permission denied' })]
    const apply = vi.spyOn(api, 'integrationApply')
    await mount()
    expect(card().textContent).toContain('cannot read config: permission denied')
    expect(card().textContent).toContain('Confirmation cannot bypass this problem')
    expect(button('Apply').disabled).toBe(true)
    expect(button('Rollback').disabled).toBe(true)
    expect(card().querySelector('[role=alertdialog]')).toBeNull()
    expect(Array.from(card().querySelectorAll('button')).some((node) => node.textContent === 'Review')).toBe(false)
    expect(button('Apply all', element).disabled).toBe(true)
    expect(button('Rollback all', element).disabled).toBe(true)
    await act(async () => card().querySelector<HTMLElement>('.lrow-note')?.click())
    expect(card().querySelector('[role=alertdialog]')).toBeNull()
    expect(apply).not.toHaveBeenCalled()
  })

  it('keeps the standing problem after a stale action error and allows review again', async () => {
    const apply = vi.spyOn(api, 'integrationApply').mockRejectedValue(new Error('config changed before commit'))
    await mount()
    await click('Apply')
    await click('Reconnect')
    expect(apply).toHaveBeenCalledWith('omp', true)
    expect(card().querySelector('[role=alert]')?.textContent).toContain('config changed before commit')
    expect(card().querySelector('.lrow-note')?.textContent).toContain(rows[0]?.detail)
    await click('Cancel')
    await click('Review')
    expect(card().querySelector('[role=alertdialog]')).not.toBeNull()
  })

  it('ignores a busy problem row click while the auto-apply toggle is in flight', async () => {
    let resolveToggle: ((value: { generation: number; enabled: boolean }) => void) | undefined
    vi.spyOn(api, 'integrationToggle').mockReturnValue(new Promise((resolve) => { resolveToggle = resolve }))
    await mount()
    const toggle = card().querySelector<HTMLInputElement>('input[type=checkbox]')
    expect(toggle).not.toBeNull()
    await act(async () => toggle?.click())
    expect(button('Review').disabled).toBe(true)
    await act(async () => card().querySelector<HTMLElement>('.lrow-note')?.click())
    expect(card().querySelector('[role=alertdialog]')).toBeNull()
    await act(async () => resolveToggle?.({ generation: 2, enabled: false }))
    await click('Review')
    expect(card().querySelector('[role=alertdialog]')).not.toBeNull()
  })

  it.each([true, false])('shows every returned backup after reconnect ok=%s', async (ok) => {
    const paths = ['/saved/settings.json.42.bak', '/saved/cache.43.bak', '/saved/catalog.44.bak', '/saved/legacy-env.45.bak', '/saved/legacy-cache.46.bak', '/saved/journal.47.bak']
    const result: IntegrationApplyResult = ok
      ? { ok: true, id: 'claude', backup: paths[5], backups: paths }
      : { ok: false, id: 'claude', reason: 'write failed after preserving current state', backup: paths[5], backups: paths }
    rows = [status({ id: 'claude' })]
    vi.spyOn(api, 'integrationApply').mockResolvedValue(result)
    await mount()
    await click('Apply')
    await click('Reconnect')
    expect(backupPaths()).toEqual(paths)
    expect(card().textContent).toContain(ok ? 'Applied; backups saved' : 'Operation failed; backups saved')
    if (!ok) expect(card().querySelector('[role=alert]')?.textContent).toContain(result.ok ? '' : result.reason)
  })

  it.each([
    { id: 'claude', label: 'Remove Prism fields' },
    { id: 'codex', label: 'Remove Prism routing' },
    { id: 'omp', label: 'Remove Prism block' },
  ] satisfies readonly { readonly id: IntegrationId; readonly label: string }[])('confirms narrow removal for $id and displays legacy failure backups', async ({ id, label }) => {
    rows = [status({ id, managed: false })]
    const rollback = vi.spyOn(api, 'integrationRollback').mockResolvedValue({ ok: false, id, reason: 'remove failed', backup: '/saved/current-config.bak' })
    await mount()
    await click('Rollback')
    expect(rollback).not.toHaveBeenCalled()
    const dialog = card().querySelector('[role=alertdialog]')
    expect(dialog?.textContent).toContain('Earlier originals are not restored')
    expect(dialog?.textContent).toContain('Unknown side files may remain')
    await click(label)
    expect(rollback).toHaveBeenCalledWith(id, true)
    expect(backupPaths()).toEqual(['/saved/current-config.bak'])
  })

  it('keeps normal Apply direct and preserves backups from bulk success and failure', async () => {
    rows = [status({ id: 'omp', managed: false, drift: false, conflict: undefined }), status({ id: 'opencode', managed: false, drift: false, conflict: undefined })]
    const apply = vi.spyOn(api, 'integrationApply').mockImplementation(async (id) => id === 'omp'
      ? { ok: true, id: 'omp', backups: ['/saved/first.bak'] }
      : { ok: false, id: 'opencode', reason: 'write refused', backups: ['/saved/second.bak'] })
    await mount()
    await click('Apply')
    expect(apply).toHaveBeenCalledWith('omp', false)
    expect(card().querySelector('[role=alertdialog]')).toBeNull()
    await click('Apply all', element)
    const paths = Array.from(element.querySelectorAll('.lrow-note')).find((node) => node.textContent?.includes('Bulk operation backups'))
    expect(Array.from(paths?.querySelectorAll('dd') ?? []).map((node) => node.textContent)).toEqual(['/saved/first.bak', '/saved/second.bak'])
    expect(element.textContent).toContain('write refused')
  })
})
