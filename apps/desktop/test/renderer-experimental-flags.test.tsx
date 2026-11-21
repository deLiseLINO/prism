// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { EXPERIMENTAL_FLAGS, ExperimentalFlagsProvider } from '../renderer/src/experimental'
import { InstallCell } from '../renderer/src/components/InstallCell'
import { ExperimentalView } from '../renderer/src/views/ExperimentalView'
import { api } from '../renderer/src/api'
import type { AgentStatus } from '@prism/contracts'

const status: AgentStatus = {
  id: 'codex',
  key: 'codex',
  installed: true,
  source: 'npm',
  canUpdate: true,
  job: { agent: 'codex', op: '', state: 'idle' },
} as AgentStatus

vi.mock('../renderer/src/bridge', () => ({
  bridge: {
    agents: {
      install: vi.fn(),
      update: vi.fn(),
      job: vi.fn(),
      status: vi.fn(),
    },
    updater: { setRcChannel: vi.fn(async () => undefined) },
  },
}))

let root: Root
let element: HTMLDivElement
let daemonFlags: Record<string, boolean>

beforeEach(() => {
  Reflect.set(globalThis, 'IS_REACT_ACT_ENVIRONMENT', true)
  daemonFlags = {}
  vi.spyOn(api, 'experimental').mockImplementation(async () => ({ flags: { ...daemonFlags } }))
  vi.spyOn(api, 'setExperimentalFlag').mockImplementation(async (flag, enabled) => {
    if (enabled) daemonFlags[flag] = true
    else delete daemonFlags[flag]
    return { flags: { ...daemonFlags } }
  })
  element = document.createElement('div')
  document.body.appendChild(element)
  root = createRoot(element)
})

afterEach(() => {
  act(() => root.unmount())
  element.remove()
  window.localStorage.clear()
  vi.restoreAllMocks()
})

async function mount(children: ReactNode): Promise<void> {
  await act(async () => {
    root.render(<ExperimentalFlagsProvider>{children}</ExperimentalFlagsProvider>)
  })
}

function buttons(): string[] {
  return Array.from(element.querySelectorAll('button')).map((node) => node.textContent?.trim() ?? '')
}

function flagCard(label: string): HTMLLabelElement {
  const card = Array.from(element.querySelectorAll<HTMLLabelElement>('.experimental-flag')).find((node) => node.textContent?.includes(label))
  if (card === undefined) throw new Error(`Missing flag card ${label}`)
  return card
}

describe('experimental flags', () => {
  it('lists every flag card, agent install first', async () => {
    await mount(<ExperimentalView />)
    expect(EXPERIMENTAL_FLAGS.map((f) => f.flag)).toEqual(['agentActions', 'otherAgents', 'visionSidecar', 'remoteInstall', 'rcChannel'])
    expect(element.textContent).toContain('Agent install and update')
    expect(element.textContent).toContain('Remote machines and daemon install')
  })

  it('hides the install and update buttons until the daemon reports the flag on', async () => {
    await mount(<InstallCell status={status} onChanged={() => {}} />)
    expect(buttons()).toEqual([])
    expect(element.textContent).toContain('npm')

    act(() => root.unmount())
    root = createRoot(element)
    daemonFlags = { agentActions: true }
    await mount(<InstallCell status={status} onChanged={() => {}} />)
    expect(buttons()).toEqual(expect.arrayContaining(['Reinstall', 'Update']))
  })

  it('stores a switched flag in the daemon so another window sees it', async () => {
    await mount(<ExperimentalView />)
    const checkbox = flagCard('Other agents').querySelector<HTMLInputElement>('input[type=checkbox]')
    expect(checkbox?.checked).toBe(false)
    await act(async () => flagCard('Other agents').querySelector<HTMLElement>('.card__title')?.click())
    expect(checkbox?.checked).toBe(true)
    expect(daemonFlags).toEqual({ otherAgents: true })

    act(() => root.unmount())
    root = createRoot(element)
    await mount(<ExperimentalView />)
    expect(flagCard('Other agents').querySelector<HTMLInputElement>('input[type=checkbox]')?.checked).toBe(true)
  })

  it('puts the switch back when the daemon refuses the write', async () => {
    vi.mocked(api.setExperimentalFlag).mockRejectedValueOnce(new Error('daemon down'))
    await mount(<ExperimentalView />)
    await act(async () => flagCard('Vision sidecar').querySelector<HTMLElement>('.card__title')?.click())
    expect(flagCard('Vision sidecar').querySelector<HTMLInputElement>('input[type=checkbox]')?.checked).toBe(false)
  })

  it('moves flags a window kept locally into the daemon once', async () => {
    window.localStorage.setItem('prism-experimental', JSON.stringify({ remoteInstall: true, rcChannel: false }))
    await mount(<ExperimentalView />)
    expect(daemonFlags).toEqual({ remoteInstall: true })
    expect(window.localStorage.getItem('prism-experimental')).toBeNull()
    expect(flagCard('Remote machines').querySelector<HTMLInputElement>('input[type=checkbox]')?.checked).toBe(true)
  })

  it('keeps the local flags when the daemon is not reachable yet', async () => {
    vi.mocked(api.experimental).mockRejectedValueOnce(new Error('daemon down'))
    window.localStorage.setItem('prism-experimental', JSON.stringify({ remoteInstall: true }))
    vi.useFakeTimers()
    try {
      await mount(<ExperimentalView />)
      expect(window.localStorage.getItem('prism-experimental')).not.toBeNull()
      await act(async () => vi.advanceTimersByTimeAsync(2000))
      expect(daemonFlags).toEqual({ remoteInstall: true })
      expect(window.localStorage.getItem('prism-experimental')).toBeNull()
    } finally {
      vi.useRealTimers()
    }
  })
})
