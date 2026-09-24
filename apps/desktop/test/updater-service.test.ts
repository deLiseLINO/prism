import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { EventEmitter } from 'node:events'
import type { AppUpdater } from 'electron-updater'
import type { UpdaterStatus } from '@prism/contracts'

class FakeAutoUpdater extends EventEmitter implements AppUpdater {
  autoDownload = false
  autoInstallOnAppQuit = true
  allowPrerelease = false
  forceDevUpdateConfig = false
  allowDowngrade = false
  disableDifferentialDownload = false
  autoRunAppAfterInstall = true
  currentVersion = '1.0.0'
  channel = 'latest'
  downloadedUpdateHelper: unknown = null
  app: unknown = null
  updateInfoAndProvider: unknown = null
  isUpdaterActive = false
  readonly logger: unknown = null
  quitAndInstallSpy = vi.fn()
  checkForUpdatesSpy = vi.fn()

  checkForUpdates(): Promise<unknown> {
    this.checkForUpdatesSpy()
    this.emit('checking-for-update')
    return Promise.resolve({})
  }

  downloadUpdate(): Promise<null> {
    return Promise.resolve(null)
  }

  quitAndInstall(isSilent: boolean, isForceRunAfter: boolean): void {
    this.quitAndInstallSpy(isSilent, isForceRunAfter)
  }

  getFeedURL(): string {
    return ''
  }
}

async function makeService(autoUpdater: FakeAutoUpdater | null, currentVersion = '1.0.0', canInstall?: () => boolean) {
  const { UpdaterService } = await import('../main/updater/updater')
  const quitApp = vi.fn()
  const service = new UpdaterService(
    { currentVersion, policy: { initialCheckDelayMs: 1000, pollIntervalMs: 5000 } },
    { autoUpdater, quitApp, canInstall },
  )
  return { service, quitApp }
}

function record(service: { subscribe: (listener: (status: UpdaterStatus) => void) => () => void }): UpdaterStatus[] {
  const statuses: UpdaterStatus[] = []
  service.subscribe((status) => statuses.push(status))
  return statuses
}

describe('UpdaterService', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.resetModules()
  })

  it('without an autoUpdater the service is permanently disabled', async () => {
    const { service } = await makeService(null)
    const statuses = record(service)
    service.start()
    expect(service.status.state).toBe('disabled')
    expect(statuses.map((s) => s.state)).toEqual(['disabled'])
    await service.check()
    expect(service.status.state).toBe('disabled')
  })

  it('configures automatic download without installing on app quit', async () => {
    const fake = new FakeAutoUpdater()
    await makeService(fake)
    expect(fake.autoDownload).toBe(true)
    expect(fake.autoInstallOnAppQuit).toBe(false)
  })

  it('keeps beta builds on the GitHub prerelease channel without downgrades', async () => {
    const fake = new FakeAutoUpdater()
    await makeService(fake, '1.0.0-beta.1')
    expect(fake.channel).toBe('beta')
    expect(fake.allowPrerelease).toBe(true)
    expect(fake.allowDowngrade).toBe(false)
  })

  it('first poll fires after the initial delay, then re-arms at the poll interval', async () => {
    const fake = new FakeAutoUpdater()
    const { service } = await makeService(fake)
    service.start()
    expect(fake.checkForUpdatesSpy).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(1000)
    expect(fake.checkForUpdatesSpy).toHaveBeenCalledTimes(1)
    expect(service.status.state).toBe('checking')
    fake.emit('update-not-available', { version: '1.0.0' })
    expect(service.status.state).toBe('up-to-date')
    await vi.advanceTimersByTimeAsync(4999)
    expect(fake.checkForUpdatesSpy).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1)
    expect(fake.checkForUpdatesSpy).toHaveBeenCalledTimes(2)
  })

  it('translates the full happy path through the emitter into status', async () => {
    const fake = new FakeAutoUpdater()
    const { service } = await makeService(fake)
    const statuses = record(service)
    service.start()
    await vi.advanceTimersByTimeAsync(1000)
    fake.emit('update-available', { version: '1.2.0' })
    fake.emit('download-progress', { percent: 42 })
    fake.emit('update-downloaded', { version: '1.2.0' })
    expect(service.status.state).toBe('downloaded')
    expect(service.status.downloadedVersion).toBe('1.2.0')
    expect(service.status.progress).toBe(100)
    const last = statuses[statuses.length - 1]
    expect(last.state).toBe('downloaded')
    expect(last.availableVersion).toBe('1.2.0')
  })

  it('install() throws unless a download is staged', async () => {
    const fake = new FakeAutoUpdater()
    const { service, quitApp } = await makeService(fake)
    expect(() => service.install()).toThrow(/no downloaded update/)
    expect(quitApp).not.toHaveBeenCalled()
  })

  it('does not quit or remove a Linux AppImage if its directory became unwritable', async () => {
    const fake = new FakeAutoUpdater()
    let writable = true
    const { service, quitApp } = await makeService(fake, '1.0.0', () => writable)
    service.start()
    await vi.advanceTimersByTimeAsync(1000)
    fake.emit('update-available', { version: '1.2.0' })
    fake.emit('update-downloaded', { version: '1.2.0' })
    writable = false
    expect(() => service.install()).toThrow(/directory is not writable/)
    expect(quitApp).not.toHaveBeenCalled()
    expect(fake.quitAndInstallSpy).not.toHaveBeenCalled()
  })

  it('install() quits the app; pendingInstall drives quitAndInstall with forceRunAfter', async () => {
    const fake = new FakeAutoUpdater()
    const { service, quitApp } = await makeService(fake)
    service.start()
    await vi.advanceTimersByTimeAsync(1000)
    fake.emit('update-available', { version: '1.2.0' })
    fake.emit('update-downloaded', { version: '1.2.0' })
    service.install()
    expect(service.status.state).toBe('installing')
    expect(service.pendingInstall).toBe(true)
    expect(quitApp).toHaveBeenCalledTimes(1)
    service.performInstall()
    expect(fake.quitAndInstallSpy).toHaveBeenCalledWith(false, true)
  })

  it('emitter errors while downloading map to error(download)', async () => {
    const fake = new FakeAutoUpdater()
    const { service } = await makeService(fake)
    service.start()
    await vi.advanceTimersByTimeAsync(1000)
    fake.emit('update-available', { version: '1.2.0' })
    fake.emit('error', new Error('checksum failed'))
    expect(service.status.state).toBe('error')
    expect(service.status.errorStage).toBe('download')
    expect(service.status.canRetry).toBe(true)
  })

  it('checkForUpdates rejection maps to error(check) and the poll continues', async () => {
    const fake = new FakeAutoUpdater()
    fake.checkForUpdates = () => {
      fake.emit('checking-for-update')
      return Promise.reject(new Error('no network'))
    }
    const { service } = await makeService(fake)
    service.start()
    await vi.advanceTimersByTimeAsync(1000)
    expect(service.status.state).toBe('error')
    expect(service.status.errorStage).toBe('check')
    await vi.advanceTimersByTimeAsync(5000)
    expect(service.status.state).toBe('error')
  })

  it('dispose clears pending timers', async () => {
    const fake = new FakeAutoUpdater()
    const { service } = await makeService(fake)
    service.start()
    service.dispose()
    await vi.advanceTimersByTimeAsync(100_000)
    expect(fake.checkForUpdatesSpy).not.toHaveBeenCalled()
  })
})
