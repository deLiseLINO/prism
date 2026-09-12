import { describe, expect, it, vi } from 'vitest'
import { renderToString } from 'react-dom/server'
import type { UpdaterStatus } from '@prism/contracts'
import { UpdateMini } from '../renderer/src/components/UpdateMini'

vi.mock('../renderer/src/bridge', () => ({
  bridge: {
    updater: {
      status: vi.fn(),
      check: vi.fn(),
      install: vi.fn(),
      onStatus: vi.fn(() => () => undefined),
    },
  },
}))

import { bridge } from '../renderer/src/bridge'

function status(overrides: Partial<UpdaterStatus>): UpdaterStatus {
  return {
    state: 'idle',
    currentVersion: '1.2.3',
    availableVersion: null,
    downloadedVersion: null,
    progress: null,
    error: null,
    errorStage: null,
    canRetry: false,
    lastCheckedAt: null,
    ...overrides,
  }
}

describe('update mini row', () => {
  it('renders ready text and a primary Restart button when downloaded', () => {
    const html = renderToString(<UpdateMini status={status({ state: 'downloaded', downloadedVersion: '1.3.0' })} />)
    expect(html).toContain('update 1.3.0 ready')
    expect(html).toContain('btn btn--primary')
    expect(html).toContain('Restart')
  })

  it('renders the rounded percent while downloading', () => {
    const html = renderToString(<UpdateMini status={status({ state: 'downloading', progress: 63.4 })} />)
    expect(html).toContain('downloading')
    expect(html).toMatch(/63<!-- -->%/)
  })

  it('renders checking with a pulsing info dot', () => {
    const html = renderToString(<UpdateMini status={status({ state: 'checking' })} />)
    expect(html).toContain('checking')
    expect(html).toContain('dot dot-info dot-pulse')
  })

  it('renders the available version', () => {
    const html = renderToString(<UpdateMini status={status({ state: 'available', availableVersion: '2.0.0' })} />)
    expect(html).toContain('update 2.0.0')
  })

  it('renders installing with a warn dot', () => {
    const html = renderToString(<UpdateMini status={status({ state: 'installing' })} />)
    expect(html).toContain('installing')
    expect(html).toContain('dot dot-warn')
  })

  it('renders nothing for idle, up-to-date, disabled and null status', () => {
    expect(renderToString(<UpdateMini status={status({ state: 'idle' })} />)).toBe('')
    expect(renderToString(<UpdateMini status={status({ state: 'up-to-date' })} />)).toBe('')
    expect(renderToString(<UpdateMini status={status({ state: 'disabled' })} />)).toBe('')
    expect(renderToString(<UpdateMini status={null} />)).toBe('')
  })

  it('renders a retryable button row on error with canRetry', () => {
    const html = renderToString(<UpdateMini status={status({ state: 'error', error: 'boom', canRetry: true })} />)
    expect(html).toContain('update error')
    expect(html).toContain('dot dot-danger')
    expect(html).toContain('<button')
    expect(html).toContain('title="boom"')
    expect(vi.mocked(bridge.updater.check)).not.toHaveBeenCalled()
  })

  it('renders plain text on error without canRetry', () => {
    const html = renderToString(<UpdateMini status={status({ state: 'error', error: 'boom', canRetry: false })} />)
    expect(html).toContain('update error')
    expect(html).not.toContain('<button')
    expect(html).toContain('title="boom"')
  })
})
