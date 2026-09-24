import type { AppUpdater } from 'electron-updater'
import type { Unsubscribe, UpdaterStatus } from '@prism/contracts'
import { DEFAULT_UPDATE_POLICY, initialModel, statusOf, step, type UpdateEffect, type UpdateEvent, type UpdatePolicy } from './machine'

export interface UpdaterOptions {
  readonly currentVersion: string
  readonly updateUrl: string | null
  readonly policy?: UpdatePolicy
}

export interface UpdaterDependencies {
  readonly autoUpdater: AppUpdater | null
  readonly quitApp: () => void
}

const QUIT_AND_INSTALL_ERROR = 'prism: no downloaded update to install'

export class UpdaterService {
  private model = initialModel('0.0.0')
  private readonly listeners = new Set<(status: UpdaterStatus) => void>()
  private timer: NodeJS.Timeout | null = null
  private readonly autoUpdater: AppUpdater | null
  private quit: () => void
  private readonly policy: UpdatePolicy

  constructor(
    private readonly options: UpdaterOptions,
    deps: UpdaterDependencies,
  ) {
    this.autoUpdater = deps.autoUpdater
    this.quit = deps.quitApp
    this.model = initialModel(options.currentVersion)
    this.policy = options.policy ?? DEFAULT_UPDATE_POLICY
    if (deps.autoUpdater !== null) {
      deps.autoUpdater.autoDownload = true
      deps.autoUpdater.autoInstallOnAppQuit = false
      if (options.updateUrl !== null) deps.autoUpdater.setFeedURL({ provider: 'generic', url: options.updateUrl })
      if (options.currentVersion.includes('-')) {
        deps.autoUpdater.channel = 'prerelease'
        deps.autoUpdater.allowDowngrade = false
      }
      this.wireEmitter(deps.autoUpdater)
    }
  }

  get status(): UpdaterStatus {
    return statusOf(this.model)
  }

  get pendingInstall(): boolean {
    return this.model.state === 'installing'
  }

  subscribe(listener: (status: UpdaterStatus) => void): Unsubscribe {
    this.listeners.add(listener)
    return () => {
      this.listeners.delete(listener)
    }
  }

  start(): void {
    if (this.autoUpdater === null) {
      this.dispatch({ type: 'disable' })
      return
    }
    this.armTimer(this.policy.initialCheckDelayMs)
  }

  async check(): Promise<void> {
    if (this.autoUpdater === null) return
    this.dispatch({ type: 'check-request' })
  }

  install(): void {
    if (this.model.state !== 'downloaded') {
      throw new Error(QUIT_AND_INSTALL_ERROR)
    }
    this.dispatch({ type: 'install-request' })
  }

  performInstall(): void {
    if (this.autoUpdater === null) return
    try {
      this.autoUpdater.quitAndInstall(false, true)
    } catch (error) {
      console.error('prism: updater quitAndInstall failed, falling back to app.quit()', error)
      this.dispatch({ type: 'install-failure', error: String(error) })
    }
  }

  dispose(): void {
    this.clearTimer()
    this.listeners.clear()
  }

  private dispatch(event: UpdateEvent): void {
    const before = this.model
    const result = step(this.model, event, this.policy)
    this.model = result.model
    this.interpret(result.effect)
    if (this.model !== before) this.emit()
  }

  private interpret(effect: UpdateEffect): void {
    switch (effect.kind) {
      case 'none':
        return
      case 'begin-check':
        void this.beginCheck()
        return
      case 'schedule-check':
        this.armTimer(effect.delayMs)
        return
      case 'quit-for-install':
        this.quit()
        return
    }
  }

  private async beginCheck(): Promise<void> {
    if (this.autoUpdater === null) return
    this.dispatch({ type: 'check-started' })
    try {
      await this.autoUpdater.checkForUpdates()
    } catch (error) {
      this.dispatch(
        this.model.state === 'downloading' || this.model.state === 'available'
          ? { type: 'download-failure', error: String(error) }
          : { type: 'check-failure', error: String(error) },
      )
    }
  }

  private armTimer(delayMs: number): void {
    this.clearTimer()
    this.timer = setTimeout(() => this.dispatch({ type: 'check-due' }), delayMs)
  }

  private clearTimer(): void {
    if (this.timer !== null) clearTimeout(this.timer)
    this.timer = null
  }

  private wireEmitter(autoUpdater: AppUpdater): void {
    autoUpdater.on('checking-for-update', () => this.dispatch({ type: 'check-started' }))
    autoUpdater.on('update-available', (info: { version?: string }) => {
      if (typeof info?.version !== 'string') return
      this.dispatch({ type: 'update-available', version: info.version })
      this.dispatch({ type: 'download-started' })
    })
    autoUpdater.on('update-not-available', () => this.dispatch({ type: 'no-update' }))
    autoUpdater.on('download-progress', (progress: { percent?: number }) => {
      if (typeof progress?.percent === 'number') this.dispatch({ type: 'download-progress', percent: progress.percent })
    })
    autoUpdater.on('update-downloaded', (info: { version?: string }) => {
      const version = typeof info?.version === 'string' ? info.version : this.model.availableVersion ?? this.model.currentVersion
      this.dispatch({ type: 'download-complete', version })
    })
    autoUpdater.on('error', (error: Error) => {
      this.dispatch(
        this.model.state === 'downloading' || this.model.state === 'available'
          ? { type: 'download-failure', error: String(error) }
          : { type: 'check-failure', error: String(error) },
      )
    })
  }

  private emit(): void {
    const status = this.status
    for (const listener of [...this.listeners]) listener(status)
  }

}
