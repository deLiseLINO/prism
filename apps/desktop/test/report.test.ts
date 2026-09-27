import { describe, expect, it, vi } from 'vitest'
import type { DaemonStatus } from '@prism/contracts'
import { buildSnapshot, sendReport, type ReportSource } from '../main/report'

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

function source(workerUrl: string | null = 'https://reports.example/report'): ReportSource {
  return {
    status,
    logTail: () => 'daemon stderr line',
    workerUrl,
    version: '0.1.0-rc',
    platform: 'win32 x64',
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
      return new Response(null, { status: 204 })
    }))
    await sendReport(source(), 'issue', 'Login failed to start', 'unknown: fetch failed')
    await sendReport(source(), 'bot', 'Login failed to start', 'unknown: fetch failed')
    expect(bodies[0]).not.toContain('daemon stderr line')
    expect(bodies[1]).toContain('daemon stderr line')
    expect(sendReport(source(null), 'issue', 'Login failed to start', 'unknown: fetch failed')).rejects.toThrow(/not configured/)
  })
})
