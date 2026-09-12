import { describe, expect, it, vi } from 'vitest'
import { renderToString } from 'react-dom/server'
import { EXPERIMENTAL_FLAGS, ExperimentalFlagsProvider, useExperimentalFlags } from '../renderer/src/experimental'
import { InstallCell } from '../renderer/src/components/InstallCell'
import { ExperimentalView, experimentalCards } from '../renderer/src/views/ExperimentalView'
import type { AgentsView, AgentStatus } from '@prism/contracts'

const agentsView = (actionsEnabled: boolean): AgentsView => ({
  agents: [status],
  actionsEnabled,
})

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

import { bridge } from '../renderer/src/bridge'

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
  it('is not in the always-visible experimental list; it is gated on the daemon switch', () => {
    expect(EXPERIMENTAL_FLAGS.map((f) => f.flag)).toEqual(['remoteInstall'])
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

  describe('experimental screen card visibility', () => {
    it('the card set excludes agent actions unless the daemon allows them', () => {
      expect(experimentalCards(false).map((c) => c.flag)).toEqual(['remoteInstall'])
    })

    it('the card set leads with agent actions when the daemon allows them', () => {
      expect(experimentalCards(true).map((c) => c.flag)).toEqual(['agentActions', 'remoteInstall'])
    })

    it('SSR (effects never ran) renders only the always-visible flags', () => {
      stubStorage({ agentActions: true })
      vi.mocked(bridge.agents.status).mockResolvedValue(agentsView(true))
      const html = renderToString(
        <ExperimentalFlagsProvider>
          <ExperimentalView />
        </ExperimentalFlagsProvider>,
      )
      expect(html).toContain('Remote machines and daemon install')
      expect(html).not.toContain('Agent install and update')
    })
  })
})
