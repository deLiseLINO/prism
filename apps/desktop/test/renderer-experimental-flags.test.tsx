// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { renderToString } from 'react-dom/server'
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { EXPERIMENTAL_FLAGS, ExperimentalFlagsProvider, useExperimentalFlags } from '../renderer/src/experimental'
import { InstallCell } from '../renderer/src/components/InstallCell'
import { ExperimentalView } from '../renderer/src/views/ExperimentalView'
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
  },
}))


function Probe(): JSX.Element {
  const { flags, setFlag } = useExperimentalFlags()
  return (
    <div>
      <span data-testid="agent-actions">{flags.agentActions ? 'on' : 'off'}</span>
      <span data-testid="remote-install">{flags.remoteInstall ? 'on' : 'off'}</span>
      <button type="button" onClick={() => setFlag('agentActions', true)}>turn on</button>
    </div>
  )
}

function stubStorage(payload: Record<string, boolean> | null): void {
  Object.defineProperty(globalThis, 'window', {
    configurable: true,
    value: {
      localStorage: {
        getItem: () => (payload === null ? null : JSON.stringify(payload)),
        setItem: () => {},
      },
    },
    writable: true,
  })
}

describe('agent actions feature flag', () => {
  it('is listed first in the experimental cards', () => {
    expect(EXPERIMENTAL_FLAGS.map((f) => f.flag)).toEqual(['agentActions', 'otherAgents', 'visionSidecar', 'remoteInstall', 'rcChannel'])
  })

  it('stays off by default and honors a stored true like any real flag', () => {
    stubStorage(null)
    const off = renderToString(
      <ExperimentalFlagsProvider>
        <Probe />
      </ExperimentalFlagsProvider>,
    )
    expect(off).toContain('data-testid="agent-actions">off<')

    stubStorage({ agentActions: true })
    const on = renderToString(
      <ExperimentalFlagsProvider>
        <Probe />
      </ExperimentalFlagsProvider>,
    )
    expect(on).toContain('data-testid="agent-actions">on<')
  })

  it('hides the install and update buttons when the flag is off', () => {
    stubStorage(null)
    const html = renderToString(
      <ExperimentalFlagsProvider>
        <InstallCell status={status} onChanged={() => {}} />
      </ExperimentalFlagsProvider>,
    )
    expect(html).not.toContain('Install<')
    expect(html).not.toContain('Update<')
    expect(html).toContain('npm')
  })

  it('shows the install and update buttons when the flag is on', () => {
    stubStorage({ agentActions: true })
    const html = renderToString(
      <ExperimentalFlagsProvider>
        <InstallCell status={status} onChanged={() => {}} />
      </ExperimentalFlagsProvider>,
    )
    expect(html).toContain('Reinstall<')
    expect(html).toContain('Update<')
  })

  it.each([
    { installed: false, error: 'required tool is not available on PATH', expected: 'required tool is not available on PATH' },
    { installed: true, error: 'native installation requires manual maintenance', expected: 'native installation requires manual maintenance' },
    { installed: false, error: undefined, expected: 'operation unsupported; check installation requirements' },
    { installed: false, error: '', expected: 'operation unsupported; check installation requirements' },
  ])('renders a stored unsupported reason with installed=$installed and error=$error even with actions hidden', ({ installed, error, expected }) => {
    stubStorage(null)
    const container = document.createElement('div')
    container.innerHTML = renderToString(
      <ExperimentalFlagsProvider>
        <InstallCell status={{ ...status, installed, canUpdate: false, job: { agent: 'codex', op: 'install', state: 'unsupported', error } }} onChanged={() => {}} />
      </ExperimentalFlagsProvider>,
    )
    expect(container.querySelector('[role=alert]')?.textContent?.trim()).toBe(expected)
    expect(container.querySelector('button')).toBeNull()
  })

  describe('experimental screen card visibility', () => {
    it('lists every flag card, agent install first, with no daemon gate', () => {
      stubStorage(null)
      const html = renderToString(
        <ExperimentalFlagsProvider>
          <ExperimentalView />
        </ExperimentalFlagsProvider>,
      )
      expect(EXPERIMENTAL_FLAGS.map((c) => c.flag)).toEqual(['agentActions', 'otherAgents', 'visionSidecar', 'remoteInstall', 'rcChannel'])
      expect(html).toContain('Agent install and update')
      expect(html).toContain('Other agents')
      expect(html).toContain('Remote machines and daemon install')
    })

    it('switches Other agents when its card text is clicked', () => {
      stubStorage(null)
      ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
      const container = document.createElement('div')
      const root = createRoot(container)
      act(() => root.render(<ExperimentalFlagsProvider><ExperimentalView /></ExperimentalFlagsProvider>))
      const card = Array.from(container.querySelectorAll<HTMLLabelElement>('.experimental-flag'))
        .find((node) => node.textContent?.includes('Other agents'))
      const checkbox = card?.querySelector<HTMLInputElement>('input[type=checkbox]')
      expect(checkbox?.checked).toBe(false)
      act(() => card?.querySelector<HTMLElement>('.card__title')?.click())
      expect(checkbox?.checked).toBe(true)
      act(() => root.unmount())
    })
  })
})
