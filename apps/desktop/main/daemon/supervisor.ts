import { spawn, type ChildProcess } from 'node:child_process'
import { DAEMON_HEALTH_PATH, DAEMON_HOST, type DaemonExit, type DaemonStatus } from '@prism/contracts'
import { locateDaemon } from './locate'
import { waitForHealth } from './health'

export interface SupervisorOptions {
  readonly port: number
  readonly daemonConfigPath: string | null
  readonly healthTimeoutMs: number
  readonly healthIntervalMs: number
  readonly healthProbeTimeoutMs: number
  readonly stopGraceMs: number
  readonly restartBaseMs: number
  readonly restartMaxMs: number
  readonly maxRestarts: number
  readonly stabilityWindowMs: number
}

type Phase =
  | { readonly kind: 'idle' }
  | { readonly kind: 'starting'; readonly attempt: number }
  | { readonly kind: 'ready'; readonly attempt: number }
  | { readonly kind: 'backoff'; readonly attempt: number }
  | { readonly kind: 'stopping'; readonly attempt: number }
  | { readonly kind: 'stopped' }
  | { readonly kind: 'failed'; readonly reason: string }
  | { readonly kind: 'quitting' }

export class DaemonSupervisor {
  private phase: Phase = { kind: 'idle' }
  private child: ChildProcess | null = null
  private backoffTimer: NodeJS.Timeout | null = null
  private killTimer: NodeJS.Timeout | null = null
  private startupAborted = false
  private readyAt = 0
  private startedAt: string | null = null
  private lastExit: DaemonExit | null = null
  private readonly listeners = new Set<(status: DaemonStatus) => void>()

  constructor(
    private readonly endpoint: string,
    private readonly options: SupervisorOptions,
  ) {}

  get status(): DaemonStatus {
    const phase = this.phase
    return {
      state: phase.kind,
      attempt: 'attempt' in phase ? phase.attempt : 0,
      pid: this.child?.pid ?? null,
      endpoint: phase.kind === 'idle' ? null : this.endpoint,
      startedAt: this.startedAt,
      lastExit: this.lastExit,
      lastError: phase.kind === 'failed' ? phase.reason : null,
    }
  }

  subscribe(listener: (status: DaemonStatus) => void): () => void {
    this.listeners.add(listener)
    return () => {
      this.listeners.delete(listener)
    }
  }

  async start(): Promise<DaemonStatus> {
    const phase = this.phase
    if (phase.kind === 'starting' || phase.kind === 'ready') return this.status
    if (phase.kind === 'quitting') throw new Error('prism: daemon supervisor is quitting')
    this.clearTimers()
    this.startupAborted = false
    await this.launch(1)
    return this.status
  }

  async stop(): Promise<DaemonStatus> {
    this.clearTimers()
    const phase = this.phase
    if (phase.kind === 'quitting') {
      await this.awaitExit()
      return this.status
    }
    if (phase.kind === 'starting' || phase.kind === 'ready') {
      this.startupAborted = true
      this.phase = { kind: 'stopping', attempt: phase.attempt }
      this.emit()
      this.terminateChild()
      await this.awaitExit()
      return this.status
    }
    if (phase.kind === 'stopping') {
      await this.awaitExit()
      return this.status
    }
    this.phase = { kind: 'stopped' }
    this.emit()
    return this.status
  }

  async stopForQuit(): Promise<void> {
    this.clearTimers()
    this.phase = { kind: 'quitting' }
    this.emit()
    this.terminateChild()
    await this.awaitExit()
  }

  private async launch(attempt: number): Promise<void> {
    let binaryPath: string
    try {
      binaryPath = locateDaemon().path
    } catch (error) {
      this.phase = { kind: 'failed', reason: error instanceof Error ? error.message : String(error) }
      this.emit()
      return
    }
    this.phase = { kind: 'starting', attempt }
    this.startedAt = new Date().toISOString()
    this.emit()
    const args = ['--listen', `${DAEMON_HOST}:${this.options.port}`]
    if (this.options.daemonConfigPath !== null) args.push('--config', this.options.daemonConfigPath)
    const child = spawn(binaryPath, args, {
      stdio: ['ignore', 'ignore', 'inherit'],
      shell: false,
    })
    this.child = child
    child.once('error', (error) => {
      this.handleSpawnError(error)
    })
    child.once('exit', (code, signal) => {
      this.handleExit(code, signal)
    })
    const outcome = await waitForHealth({
      url: `${this.endpoint}${DAEMON_HEALTH_PATH}`,
      timeoutMs: this.options.healthTimeoutMs,
      intervalMs: this.options.healthIntervalMs,
      probeTimeoutMs: this.options.healthProbeTimeoutMs,
      isCurrent: () => this.isCurrentStart(attempt),
    })
    if (!this.isCurrentStart(attempt)) return
    if (outcome === 'healthy') {
      this.phase = { kind: 'ready', attempt }
      this.readyAt = Date.now()
      this.emit()
      return
    }
    this.terminateChild()
  }

  private isCurrentStart(attempt: number): boolean {
    return this.phase.kind === 'starting' && this.phase.attempt === attempt
  }

  private handleSpawnError(error: Error): void {
    if (this.phase.kind !== 'starting') return
    this.child = null
    this.clearTimers()
    this.phase = { kind: 'failed', reason: `prism: daemon spawn failed: ${error.message}` }
    this.emit()
  }

  private handleExit(code: number | null, signal: string | null): void {
    this.child = null
    this.clearTimers()
    this.lastExit = { code, signal }
    const phase = this.phase
    if (phase.kind === 'quitting') return
    if (phase.kind === 'stopping') {
      this.phase = { kind: 'stopped' }
      this.emit()
      return
    }
    if (phase.kind === 'starting' || phase.kind === 'ready') {
      const stable = phase.kind === 'ready' && Date.now() - this.readyAt >= this.options.stabilityWindowMs
      this.handleCrash(stable ? 0 : phase.attempt)
    }
  }

  private handleCrash(attempt: number): void {
    const exit = this.lastExit
    const detail = exit === null ? 'unknown exit' : `code=${exit.code ?? 'none'} signal=${exit.signal ?? 'none'}`
    if (attempt >= this.options.maxRestarts) {
      this.phase = { kind: 'failed', reason: `prism: daemon crashed ${attempt} times without reaching a stable ready state (${detail})` }
      this.emit()
      return
    }
    this.phase = { kind: 'backoff', attempt }
    this.emit()
    const delay = Math.min(this.options.restartBaseMs * 2 ** attempt, this.options.restartMaxMs)
    this.backoffTimer = setTimeout(() => {
      this.backoffTimer = null
      void this.launch(attempt + 1)
    }, delay)
  }

  private terminateChild(): void {
    const child = this.child
    if (child === null) return
    child.kill('SIGTERM')
    this.killTimer = setTimeout(() => {
      this.killTimer = null
      child.kill('SIGKILL')
    }, this.options.stopGraceMs)
  }

  private awaitExit(): Promise<void> {
    const child = this.child
    if (child === null) return Promise.resolve()
    const { promise, resolve } = Promise.withResolvers<void>()
    child.once('exit', () => resolve())
    return promise
  }

  private clearTimers(): void {
    if (this.backoffTimer !== null) {
      clearTimeout(this.backoffTimer)
      this.backoffTimer = null
    }
    if (this.killTimer !== null) {
      clearTimeout(this.killTimer)
      this.killTimer = null
    }
  }

  private emit(): void {
    for (const listener of [...this.listeners]) listener(this.status)
  }
}

