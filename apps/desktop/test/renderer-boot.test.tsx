import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import type { DaemonStatus } from '@prism/contracts'
import { BootScreen, resolveBootPhase } from '../renderer/src/boot'

function status(state: DaemonStatus['state'], attempt = 0): DaemonStatus {
  return {
    state,
    attempt,
    pid: null,
    endpoint: 'http://127.0.0.1:10200',
    startedAt: null,
    lastExit: null,
    lastError: null,
  }
}

describe('resolveBootPhase', () => {
  it('boots while the status is still unknown', () => {
    expect(resolveBootPhase(null, false, false)).toEqual({ kind: 'booting', label: 'waking up…' })
  })

  it('boots through idle, starting, and backoff with live labels', () => {
    expect(resolveBootPhase(status('idle'), false, false)).toEqual({ kind: 'booting', label: 'waking up…' })
    expect(resolveBootPhase(status('starting', 1), false, false)).toEqual({ kind: 'booting', label: 'starting daemon…' })
    expect(resolveBootPhase(status('backoff', 2), false, false)).toEqual({ kind: 'booting', label: 'restarting (attempt 3)…' })
  })

  it('gives up on ready, failed, unreachable, or timeout', () => {
    expect(resolveBootPhase(status('ready'), false, false)).toEqual({ kind: 'gave-up' })
    expect(resolveBootPhase(status('failed'), false, false)).toEqual({ kind: 'gave-up' })
    expect(resolveBootPhase(status('starting'), true, false)).toEqual({ kind: 'gave-up' })
    expect(resolveBootPhase(status('starting'), false, true)).toEqual({ kind: 'gave-up' })
  })
})

describe('BootScreen', () => {
  it('renders the waking-up label before the first status', () => {
    const html = renderToString(<BootScreen phase={{ kind: 'booting', label: 'waking up…' }} />)
    expect(html).toContain('waking up')
    expect(html).toContain('aria-busy')
    expect(html).toContain('boot-mark')
  })

  it('renders the restart attempt label during backoff', () => {
    const html = renderToString(<BootScreen phase={{ kind: 'booting', label: 'restarting (attempt 3)…' }} />)
    expect(html).toContain('restarting (attempt 3)')
  })

  it('renders nothing once the gate has opened', () => {
    const html = renderToString(<BootScreen phase={{ kind: 'gave-up' }} />)
    expect(html).toBe('')
  })
})
