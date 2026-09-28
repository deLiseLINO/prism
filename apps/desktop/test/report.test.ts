import { describe, expect, it, vi } from 'vitest'
import { buildSnapshot, noteWindowLine, publicBody, sendReport, windowLogTail, type ReportSource } from '../main/report'

vi.mock('electron', () => ({ app: { isPackaged: true, getVersion: () => '0.1.0' } }))

const status: DaemonStatus = {
  state: 'ready',
  attempt: 1,
  pid: 42,
  endpoint: 'http://127.0.0.1:10200',
  startedAt: '2026-09-27T18:00:00.000Z',
  lastExit: null,
  lastError: null,
}

function source(workerUrl: string | null = 'https://reports.example/report', windowLog: () => string = () => ''): ReportSource {
  return {
    status,
    logTail: () => 'daemon stderr line',
    workerUrl,
    version: '0.1.0-rc',
    platform: 'win32 x64',
    os: 'Windows_NT 10.0.26100 x64',
    windowLog,
  }
}

describe('report snapshot', () => {
  it('keeps the daemon log out of the public issue body', () => {
    const snapshot = buildSnapshot(source(), 'Login failed to start', 'unknown: fetch failed')
    expect(snapshot.body).toContain('version: 0.1.0-rc')
    expect(snapshot.body).toContain('daemon: ready')
    expect(snapshot.body).not.toContain('daemon stderr line')
    expect(snapshot.issueUrl).toContain('https://github.com/deLiseLINO/prism/issues/new')
    expect(snapshot.workerConfigured).toBe(true)
  })

  it('sends the log only to the bot', async () => {
    const bodies: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (_url: string, init: RequestInit) => {
      bodies.push(String(init.body))
      return Response.json({ url: 'https://reports.example/r?key=secret' })
    }))
    await sendReport(source(), 'bot', 'Login failed to start', 'unknown: fetch failed', null)
    expect(bodies[0]).toContain('daemon stderr line')
    expect(sendReport(source(null), 'bot', 'Login failed to start', 'unknown: fetch failed', null)).rejects.toThrow(/not configured/)
  })

  it('keeps os, the newest steps, and the failed request inside 6000', () => {
    const steps = Array.from({ length: 8 }, (_, index) => ({
      kind: 'navigate' as const,
      view: `view-${index}`,
      at: Date.UTC(2026, 8, 28, 18, 2, index),
    }))
    const body = publicBody(source(), 'Login failed to start', 'x'.repeat(7_000), {
      steps,
      failed: { method: 'POST', path: '/api/v1/auth/start', status: 500, code: 'auth_failed' },
    })
    expect(body.length).toBeLessThanOrEqual(6_000)
    expect(body).toContain('os: Windows_NT 10.0.26100 x64')
    expect(body).toContain('request: POST /api/v1/auth/start 500 auth_failed')
    expect(body).toContain('navigate view-0')
    expect(body).toContain('navigate view-7')
    expect(body.startsWith('detail:')).toBe(false)
    const stepsLine = body.split('\n').find((line) => line.startsWith('steps:'))
    expect(stepsLine).toBeDefined()
    expect(stepsLine?.slice('steps: '.length)).not.toContain(': ')
  })
  it('drops the oldest step before the failed request', () => {
    const steps = Array.from({ length: 8 }, (_, index) => ({
      kind: 'navigate' as const,
      view: `view-${index}-${'n'.repeat(30)}`,
      at: Date.UTC(2026, 8, 28, 18, 2, index),
    }))
    const crowded = source()
    const body = publicBody({ ...crowded, status: { ...crowded.status, lastError: 'e'.repeat(5_400) } }, 'Login failed to start', 'x'.repeat(7_000), {
      steps,
      failed: { method: 'POST', path: '/api/v1/auth/start', status: 500, code: 'auth_failed' },
    })
    expect(body.length).toBeLessThanOrEqual(6_000)
    expect(body).toContain('request: POST /api/v1/auth/start 500 auth_failed')
    expect(body).not.toContain('navigate view-0-')
    expect(body).toContain('navigate view-7-')
  })

  it('omits steps and request when the context is empty', () => {
    const snapshot = buildSnapshot(source(), 'Login failed to start', 'unknown: fetch failed')
    expect(snapshot.body).toContain('os: Windows_NT 10.0.26100 x64')
    expect(snapshot.body).not.toContain('steps:')
    expect(snapshot.body).not.toContain('request:')
  })

  it('appends a 64KB window tail after the daemon log', async () => {
    noteWindowLine('a'.repeat(70_000))
    noteWindowLine('renderer console line')
    const bodies: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (_url: string, init: RequestInit) => {
      bodies.push(String(init.body))
      return Response.json({ url: 'https://reports.example/r?key=secret' })
    }))
    await sendReport(source(undefined, windowLogTail), 'bot', 'Login failed to start', 'unknown: fetch failed', null, {
      steps: [{ kind: 'action', name: 'auth-start', at: Date.UTC(2026, 8, 28, 18, 2, 4) }],
      failed: null,
    })
    const payload = JSON.parse(bodies[0] ?? '{}') as { log: string; body: string }
    const windowPart = payload.log.split('\n--- window.log\n')[1] ?? ''
    expect(payload.log.startsWith('daemon stderr line\n--- window.log\n')).toBe(true)
    expect(windowPart.length).toBeLessThanOrEqual(64 * 1024)
    expect(windowPart.endsWith('renderer console line')).toBe(true)
    expect(payload.body).not.toContain('renderer console line')
    expect(payload.body).toContain('action auth-start')
    expect(payload.body).not.toContain('request:')
  })
})
