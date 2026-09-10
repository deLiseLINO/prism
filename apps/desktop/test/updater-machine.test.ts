import { describe, expect, it } from 'vitest'
import {
  DEFAULT_UPDATE_POLICY,
  initialModel,
  statusOf,
  step,
  type UpdateEvent,
  type UpdaterModel,
} from '../main/updater/machine'

const policy = { ...DEFAULT_UPDATE_POLICY, initialCheckDelayMs: 1000, pollIntervalMs: 5000 }
const FIXED_NOW = '2026-09-09T12:00:00.000Z'
const now = () => FIXED_NOW

function apply(model: UpdaterModel, ...events: UpdateEvent[]): { model: UpdaterModel; effects: string[] } {
  const effects: string[] = []
  let current = model
  for (const event of events) {
    const result = step(current, event, policy, now)
    current = result.model
    if (result.effect.kind !== 'none') {
      effects.push(result.effect.kind === 'schedule-check' ? `schedule-check:${result.effect.delayMs}` : result.effect.kind)
    }
  }
  return { model: current, effects }
}

function checked(model: UpdaterModel): UpdaterModel {
  return apply(model, { type: 'check-request' }).model
}

describe('update machine', () => {
  it('starts idle with no versions or errors', () => {
    const model = initialModel('1.0.0')
    const status = statusOf(model)
    expect(status.state).toBe('idle')
    expect(status.currentVersion).toBe('1.0.0')
    expect(status.availableVersion).toBeNull()
    expect(status.downloadedVersion).toBeNull()
    expect(status.canRetry).toBe(false)
  })

  it('disable is terminal: every other event is a no-op', () => {
    const { model } = apply(initialModel('1.0.0'), { type: 'disable' }, { type: 'check-due' }, { type: 'update-available', version: '2.0.0' })
    expect(model.state).toBe('disabled')
  })

  it('check-request from idle begins a check and clears stale errors', () => {
    const { model, effects } = apply(initialModel('1.0.0'), { type: 'check-request' })
    expect(model.state).toBe('checking')
    expect(effects).toEqual(['begin-check'])
  })

  it('check-request is a no-op while checking, available, downloading, or installing', () => {
    for (const busy of ['checking', 'available', 'downloading', 'installing'] as const) {
      const model = { ...initialModel('1.0.0'), state: busy }
      const { model: next, effects } = apply(model, { type: 'check-request' })
      expect(next.state).toBe(busy)
      expect(effects).toEqual([])
    }
  })

  it('full happy path: available, download, complete, install', () => {
    const { model, effects } = apply(
      initialModel('1.0.0'),
      { type: 'check-request' },
      { type: 'update-available', version: '1.2.0' },
      { type: 'download-started' },
      { type: 'download-progress', percent: 40 },
      { type: 'download-progress', percent: 999 },
      { type: 'download-complete', version: '1.2.0' },
      { type: 'install-request' },
    )
    expect(model.state).toBe('installing')
    expect(model.downloadedVersion).toBe('1.2.0')
    expect(model.availableVersion).toBe('1.2.0')
    expect(model.progress).toBeNull()
    expect(effects).toEqual(['begin-check', 'schedule-check:5000', 'quit-for-install'])
    expect(model.progress === null ? 100 : model.progress).toBe(100)
  })

  it('download progress is clamped to 0..100', () => {
    const afterAvailable = apply(initialModel('1.0.0'), { type: 'check-request' }, { type: 'update-available', version: '2.0.0' }, { type: 'download-started' })
    const { model } = apply(afterAvailable.model, { type: 'download-progress', percent: 999 })
    expect(model.progress).toBe(100)
  })

  it('no-update schedules the next poll', () => {
    const { model, effects } = apply(initialModel('1.0.0'), { type: 'check-request' }, { type: 'no-update' })
    expect(model.state).toBe('up-to-date')
    expect(model.availableVersion).toBeNull()
    expect(effects).toEqual(['begin-check', 'schedule-check:5000'])
  })

  it('check-failure lands in error(check) with canRetry, and a retry works', () => {
    const failed = apply(initialModel('1.0.0'), { type: 'check-request' }, { type: 'check-failure', error: 'network down' })
    expect(failed.model.state).toBe('error')
    expect(failed.model.errorStage).toBe('check')
    expect(statusOf(failed.model).canRetry).toBe(true)
    expect(failed.effects).toEqual(['begin-check', 'schedule-check:5000'])
    const retried = apply(failed.model, { type: 'check-request' })
    expect(retried.model.state).toBe('checking')
  })

  it('a staged download survives later check failures', () => {
    const downloaded = apply(
      initialModel('1.0.0'),
      { type: 'check-request' },
      { type: 'update-available', version: '1.2.0' },
      { type: 'download-started' },
      { type: 'download-complete', version: '1.2.0' },
    )
    const { model } = apply(downloaded.model, { type: 'check-request' }, { type: 'check-failure', error: 'offline' })
    expect(model.state).toBe('downloaded')
    expect(model.downloadedVersion).toBe('1.2.0')
    expect(model.error).toBeNull()
  })

  it('no-update after a staged download keeps the downloaded version visible', () => {
    const downloaded = apply(
      initialModel('1.0.0'),
      { type: 'check-request' },
      { type: 'update-available', version: '1.2.0' },
      { type: 'download-complete', version: '1.2.0' },
    )
    const { model } = apply(downloaded.model, { type: 'check-request' }, { type: 'no-update' })
    expect(model.state).toBe('downloaded')
    expect(model.availableVersion).toBe('1.2.0')
  })

  it('download-failure lands in error(download) and schedules a poll', () => {
    const { model, effects } = apply(
      initialModel('1.0.0'),
      { type: 'check-request' },
      { type: 'update-available', version: '1.2.0' },
      { type: 'download-failure', error: 'checksum mismatch' },
    )
    expect(model.state).toBe('error')
    expect(model.errorStage).toBe('download')
    expect(effects).toEqual(['begin-check', 'schedule-check:5000'])
  })

  it('install-request is rejected unless a download is staged', () => {
    const { model, effects } = apply(checked(initialModel('1.0.0')), { type: 'install-request' })
    expect(model.state).toBe('checking')
    expect(effects).toEqual([])
  })

  it('install-failure returns to error(install) with the download still staged for retry', () => {
    const installing = apply(
      initialModel('1.0.0'),
      { type: 'check-request' },
      { type: 'update-available', version: '1.2.0' },
      { type: 'download-complete', version: '1.2.0' },
      { type: 'install-request' },
    )
    const { model } = apply(installing.model, { type: 'install-failure', error: 'quit failed' })
    expect(model.state).toBe('error')
    expect(model.errorStage).toBe('install')
    expect(model.downloadedVersion).toBe('1.2.0')
    expect(statusOf(model).canRetry).toBe(true)
  })

  it('events out of phase are ignored, never destructive', () => {
    const idle = initialModel('1.0.0')
    const { model } = apply(idle, { type: 'update-available', version: '9.9.9' }, { type: 'download-complete', version: '9.9.9' }, { type: 'install-failure', error: 'x' })
    expect(model.state).toBe('idle')
    expect(model.availableVersion).toBeNull()
  })
})
