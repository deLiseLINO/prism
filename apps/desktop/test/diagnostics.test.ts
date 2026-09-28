import { afterEach, describe, expect, it, vi } from 'vitest'
import { noteFailedRequest, noteStep, reportContext } from '../renderer/src/diagnostics'

describe('renderer diagnostics', () => {
  afterEach(() => {
    vi.resetModules()
  })

  it('keeps eight steps and strips the failed path', () => {
    for (let index = 0; index < 9; index += 1) noteStep({ kind: 'navigate', view: `view-${index}` })
    noteStep({ kind: 'action', name: 'bad:name' })
    noteStep({ kind: 'action', name: '' })
    noteFailedRequest({
      method: 'POST',
      path: '/api/v1/auth/start?token=secret#frag',
      status: 500,
      code: 'auth_failed',
    })
    const context = reportContext()
    expect(context.steps).toHaveLength(8)
    expect(context.steps[0]?.kind === 'navigate' ? context.steps[0].view : '').toBe('view-1')
    expect(context.steps[7]?.kind === 'navigate' ? context.steps[7].view : '').toBe('view-8')
    expect(context.failed).toEqual({ method: 'POST', path: '/api/v1/auth/start', status: 500, code: 'auth_failed' })
  })

  it('drops a path that still contains a colon after stripping', async () => {
    vi.resetModules()
    const isolated = await import('../renderer/src/diagnostics')
    isolated.noteFailedRequest({ method: 'GET', path: 'https://user:secret@example/api?x=1', status: 500, code: 'auth_failed' })
    expect(isolated.reportContext().failed).toBeNull()
  })
})
