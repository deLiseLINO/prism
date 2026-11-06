// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest'
import type { HostInstallReply, HostInstallRequest, HostView, ProvidersView } from '@prism/contracts'
import { MachinesView, syncLabel } from '../renderer/src/views/MachinesView'
import { ActiveHostProvider, RemoteBanner } from '../renderer/src/ActiveHost'
import { api, ApiError } from '../renderer/src/api'

vi.mock('../renderer/src/bridge', () => ({ bridge: {} }))

const LOCAL: HostView = { id: 'local', local: true, status: 'ok' }

let root: Root
let element: HTMLDivElement
let hosts: HostView[]
let installDaemon: Mock<(request: HostInstallRequest) => Promise<HostInstallReply>>

beforeEach(() => {
  Reflect.set(globalThis, 'IS_REACT_ACT_ENVIRONMENT', true)
  element = document.createElement('div')
  document.body.appendChild(element)
  root = createRoot(element)
  hosts = [LOCAL]
  installDaemon = vi.fn<(request: HostInstallRequest) => Promise<HostInstallReply>>()
  Reflect.set(window, 'prism', { integrations: { installDaemon } })
  vi.spyOn(api, 'localHosts').mockImplementation(async () => ({ hosts }))
  vi.spyOn(api, 'localProviders').mockImplementation(async () => ({ generation: 7 }) as ProvidersView)
})

afterEach(() => {
  act(() => root.unmount())
  element.remove()
  Reflect.deleteProperty(window, 'prism')
  vi.restoreAllMocks()
})

async function mount(): Promise<void> {
  await act(async () => {
    root.render(
      <ActiveHostProvider>
        <RemoteBanner />
        <MachinesView />
      </ActiveHostProvider>,
    )
  })
}

function row(id: string): HTMLElement {
  const node = Array.from(element.querySelectorAll<HTMLElement>('.mach-item'))
    .find((item) => item.querySelector('.lrow__title > span')?.textContent === id)
  if (node === undefined) throw new Error(`Missing machine row ${id}`)
  return node
}

function button(scope: ParentNode, label: string): HTMLButtonElement {
  const node = Array.from(scope.querySelectorAll('button')).find((candidate) => candidate.textContent?.trim() === label)
  if (node === undefined) throw new Error(`Missing button ${label}`)
  return node
}

function type(id: string, value: string): void {
  const input = element.querySelector<HTMLInputElement>(`#${id}`)
  if (input === null) throw new Error(`Missing input ${id}`)
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set?.call(input, value)
  input.dispatchEvent(new Event('input', { bubbles: true }))
}

function installed(id: string, partial: Partial<HostView> = {}): HostView {
  return { id, local: false, status: 'ok', address: `user@${id}`, daemonPort: 10200, autoSync: true, sync: { state: 'synced', at: new Date(Date.now() - 5 * 60_000).toISOString() }, ...partial }
}

describe('adding a machine', () => {
  it('starts the install on its own and offers a retry when it fails', async () => {
    const create = vi.spyOn(api, 'localCreateHost').mockImplementation(async (body) => {
      hosts = [LOCAL, { id: body.id, local: false, status: 'not_installed', address: body.address }]
      return { generation: 8, host: hosts[1]! }
    })
    const { promise, resolve } = Promise.withResolvers<HostInstallReply>()
    installDaemon.mockReturnValue(promise)
    await mount()

    await act(async () => {
      type('machine-id', 'box')
      type('machine-address', 'user@box')
    })
    await act(async () => button(element, 'Add machine').click())

    expect(create).toHaveBeenCalledWith({ id: 'box', address: 'user@box', expectedGeneration: 7 })
    expect(installDaemon).toHaveBeenCalledWith({ host: 'box' })
    expect(button(row('box'), 'Installing…').disabled).toBe(true)
    expect(button(row('box'), 'Manage').disabled).toBe(true)

    await act(async () => resolve({ ok: false, host: 'box', phase: 'probe', reason: 'ssh probe failed: no route to host' }))

    expect(row('box').querySelector('[role=alert]')?.textContent).toBe('ssh probe failed: no route to host')
    expect(button(row('box'), 'Retry install').disabled).toBe(false)
    expect(button(row('box'), 'Manage').disabled).toBe(true)
    expect(row('box').querySelector('.mach-sync')).toBeNull()
  })
})

describe('auto sync on an installed machine', () => {
  it('turns off when the setting row is clicked and sends the current generation', async () => {
    hosts = [LOCAL, installed('edge')]
    const setAutoSync = vi.spyOn(api, 'localSetHostAutoSync').mockImplementation(async (id, body) => {
      hosts = [LOCAL, installed(id, { autoSync: body.enabled, sync: { state: 'off' } })]
      return { generation: 8, host: hosts[1]! }
    })
    await mount()
    const sync = row('edge').querySelector<HTMLElement>('.mach-sync')
    expect(sync?.textContent).toContain('Synced 5 min ago')
    expect(sync?.querySelector<HTMLInputElement>('input[type=checkbox]')?.checked).toBe(true)

    await act(async () => sync?.querySelector<HTMLElement>('.mach-sync__status')?.click())

    expect(setAutoSync).toHaveBeenCalledTimes(1)
    expect(setAutoSync).toHaveBeenCalledWith('edge', { enabled: false, expectedGeneration: 7 })
    const after = row('edge').querySelector<HTMLElement>('.mach-sync')
    expect(after?.querySelector<HTMLInputElement>('input[type=checkbox]')?.checked).toBe(false)
    expect(after?.textContent).toContain('Auto sync off')
  })

  it('shows a rejected toggle inline and keeps the previous value', async () => {
    hosts = [LOCAL, installed('edge')]
    vi.spyOn(api, 'localSetHostAutoSync').mockRejectedValue(new ApiError(409, 'stale_generation', 'config changed underneath'))
    await mount()

    await act(async () => row('edge').querySelector<HTMLElement>('.mach-sync')?.click())

    expect(row('edge').querySelector('[role=alert]')?.textContent).toBe('stale_generation: config changed underneath')
    expect(row('edge').querySelector<HTMLInputElement>('.mach-sync input[type=checkbox]')?.checked).toBe(true)
  })

  it('keeps the full failure text on hover', async () => {
    hosts = [LOCAL, installed('edge', { sync: { state: 'failed', error: 'ssh edge: connection refused' } })]
    await mount()
    const status = row('edge').querySelector<HTMLElement>('.mach-sync__status')
    expect(status?.textContent).toBe('Sync failed: ssh edge: connection refused')
    expect(status?.title).toBe('Sync failed: ssh edge: connection refused')
  })
})

describe('sync status text', () => {
  const now = Date.parse('2026-11-04T12:00:00Z')
  it.each([
    [{ state: 'synced', at: '2026-11-04T11:59:30Z' }, true, 'Synced just now'],
    [{ state: 'synced', at: '2026-11-04T09:00:00Z' }, true, 'Synced 3 h ago'],
    [{ state: 'pending' }, true, 'Waiting for first sync'],
    [undefined, true, 'Waiting for first sync'],
    [{ state: 'failed', error: 'timeout' }, true, 'Sync failed: timeout'],
    [{ state: 'off' }, false, 'Auto sync off'],
    [{ state: 'synced', at: '2026-11-04T11:00:00Z' }, false, 'Auto sync off'],
  ] as const)('maps %j with autoSync %s to %s', (sync, autoSync, label) => {
    expect(syncLabel(sync, autoSync, now)).toBe(label)
  })
})

describe('managing a remote machine', () => {
  it('warns in the banner that synced settings are replaced, only when auto sync is on', async () => {
    hosts = [LOCAL, installed('edge'), installed('workmac', { autoSync: false, sync: { state: 'off' } })]
    await mount()

    await act(async () => button(row('edge'), 'Manage').click())
    const banner = (): string => element.querySelector('.banner')?.textContent ?? ''
    expect(banner()).toContain('Managing edge')
    expect(banner()).toContain('Settings sync from this machine. Changes made here are replaced on the next sync.')

    await act(async () => button(row('workmac'), 'Manage').click())
    expect(banner()).toContain('Managing workmac')
    expect(banner()).not.toContain('Settings sync from this machine')
  })
})
