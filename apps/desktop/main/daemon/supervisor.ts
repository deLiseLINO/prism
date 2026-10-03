import { readFileSync } from 'node:fs'
import { DAEMON_HEALTH_PATH, DAEMON_HOST, type DaemonExit, type DaemonStatus } from '@prism/contracts'
import { runCli, type CliResult } from './cli'
import { locateDaemon, locateWebui, stageDaemon } from './locate'
import { probeHealth } from './health'

export interface SupervisorOptions {
  readonly port: number
  readonly daemonConfigPath: string | null
  readonly webuiDir: string | null
  readonly logPath: string | null
  readonly commandTimeoutMs: number
  readonly healthPollMs: number
  readonly healthProbeTimeoutMs: number
  readonly restartBaseMs: number
  readonly restartMaxMs: number
  readonly maxRestarts: number
  readonly stabilityWindowMs: number
}

const HEALTH_MISS_LIMIT = 3
const LOG_TAIL_LIMIT = 16_000

type Phase =
  | { readonly kind: 'idle' }
  | { readonly kind: 'starting'; readonly attempt: number }
  | { readonly kind: 'ready'; readonly attempt: number }
  | { readonly kind: 'backoff'; readonly attempt: number }
  | { readonly kind: 'stopping'; readonly attempt: number }
  | { readonly kind: 'stopped' }
  | { readonly kind: 'failed'; readonly reason: string }
  | { readonly kind: 'quitting' }

type Launch =
  | { readonly kind: 'url'; readonly url: string }
  | { readonly kind: 'retry'; readonly detail: string }
  | { readonly kind: 'fatal'; readonly reason: string }

export class DaemonSupervisor {
  private phase: Phase = { kind: 'idle' }
  private epoch = 0
  private pending: Promise<unknown> | null = null
  private backoffTimer: NodeJS.Timeout | null = null
  private pollTimer: NodeJS.Timeout | null = null
  private binary: string | null = null
  private reportedUrl: string | null = null
  private readyAt = 0
  private startedAt: string | null = null
  private lastExit: DaemonExit | null = null
  private log = ''
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
      pid: null,
      endpoint: phase.kind === 'idle' ? null : (this.reportedUrl ?? this.endpoint),
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
    return this.begin('start')
  }

  async restart(): Promise<DaemonStatus> {
    return this.begin('restart')
  }

  async stop(): Promise<DaemonStatus> {
    if (this.phase.kind === 'quitting') return this.status
    this.halt()
    await this.pending
    this.phase = { kind: 'stopping', attempt: 0 }
    this.emit()
    const stopped = this.runService(['stop'])
    this.pending = stopped
    const result = await stopped
    if (this.phase.kind !== 'stopping') return this.status
    this.phase = result.code === 0 ? { kind: 'stopped' } : { kind: 'failed', reason: failureText('service stop', result) }
    this.emit()
    return this.status
  }

  release(): void {
    this.halt()
    this.phase = { kind: 'quitting' }
    this.emit()
  }

  logTail(): string {
    return this.log
  }

  logFile(): string {
    if (this.options.logPath === null) return this.log
    try {
      return readFileSync(this.options.logPath, 'utf8')
    } catch {
      return this.log
    }
  }

  private async begin(command: 'start' | 'restart'): Promise<DaemonStatus> {
    const phase = this.phase
    if (phase.kind === 'quitting') throw new Error('prism: daemon supervisor is quitting')
    if (command === 'start' && (phase.kind === 'starting' || phase.kind === 'ready')) return this.status
    this.halt()
    await this.pending
    if (this.phase.kind === 'quitting') return this.status
    await this.launch(1, command)
    return this.status
  }

  private halt(): void {
    this.epoch++
    if (this.backoffTimer !== null) {
      clearTimeout(this.backoffTimer)
      this.backoffTimer = null
    }
    if (this.pollTimer !== null) {
      clearTimeout(this.pollTimer)
      this.pollTimer = null
    }
  }

  private async launch(attempt: number, command: 'start' | 'restart'): Promise<void> {
    const epoch = this.epoch
    this.phase = { kind: 'starting', attempt }
    this.startedAt = new Date().toISOString()
    this.emit()
    const launching = this.requestDaemon(command)
    this.pending = launching
    const outcome = await launching
    if (epoch !== this.epoch) return
    switch (outcome.kind) {
      case 'url':
        this.reportedUrl = outcome.url
        this.phase = { kind: 'ready', attempt }
        this.readyAt = Date.now()
        this.emit()
        this.schedulePoll(epoch, 0)
        return
      case 'retry':
        this.handleLoss(attempt, outcome.detail)
        return
      case 'fatal':
        this.phase = { kind: 'failed', reason: outcome.reason }
        this.emit()
    }
  }

  private async requestDaemon(command: 'start' | 'restart'): Promise<Launch> {
    const args = [command, '--listen', `${DAEMON_HOST}:${this.options.port}`]
    if (this.options.daemonConfigPath !== null) args.push('--config', this.options.daemonConfigPath)
    const webuiDir = locateWebui(this.options.webuiDir)
    if (webuiDir !== null) args.push('--webui', webuiDir)
    let result: CliResult
    try {
      result = await this.runService(args)
    } catch (error) {
      return { kind: 'fatal', reason: error instanceof Error ? error.message : String(error) }
    }
    const url = (result.stdout.split('\n', 1)[0] ?? '').trim()
    if (result.code === 0 && url !== '') return { kind: 'url', url }
    return { kind: 'retry', detail: failureText(`service ${command}`, result) }
  }

  private async runService(args: readonly string[]): Promise<CliResult> {
    this.binary ??= await stageDaemon(locateDaemon())
    const result = await runCli(this.binary, ['service', ...args], this.options.commandTimeoutMs)
    this.lastExit = { code: result.code, signal: null }
    this.appendLog(result.stderr)
    return result
  }

  private schedulePoll(epoch: number, misses: number): void {
    this.pollTimer = setTimeout(() => {
      this.pollTimer = null
      void this.poll(epoch, misses)
    }, this.options.healthPollMs)
  }

  private async poll(epoch: number, misses: number): Promise<void> {
    const healthy = await probeHealth(`${this.reportedUrl ?? this.endpoint}${DAEMON_HEALTH_PATH}`, this.options.healthProbeTimeoutMs)
    const phase = this.phase
    if (epoch !== this.epoch || phase.kind !== 'ready') return
    const next = healthy ? 0 : misses + 1
    if (next < HEALTH_MISS_LIMIT) {
      this.schedulePoll(epoch, next)
      return
    }
    const stable = Date.now() - this.readyAt >= this.options.stabilityWindowMs
    this.handleLoss(stable ? 0 : phase.attempt, `prism: the daemon at ${this.endpoint} stopped answering`)
  }

  private handleLoss(attempt: number, detail: string): void {
    if (attempt >= this.options.maxRestarts) {
      this.phase = { kind: 'failed', reason: `${detail} (gave up after ${attempt} attempts)` }
      this.emit()
      return
    }
    this.phase = { kind: 'backoff', attempt }
    this.emit()
    const delay = Math.min(this.options.restartBaseMs * 2 ** attempt, this.options.restartMaxMs)
    this.backoffTimer = setTimeout(() => {
      this.backoffTimer = null
      void this.launch(attempt + 1, 'start')
    }, delay)
  }

  private appendLog(text: string): void {
    if (text === '') return
    this.log = (this.log + text).slice(-LOG_TAIL_LIMIT)
  }

  private emit(): void {
    for (const listener of [...this.listeners]) listener(this.status)
  }
}

function failureText(command: string, result: CliResult): string {
  const detail = result.stderr.trim()
  return `prism: ${command} failed (code=${result.code ?? 'none'})${detail === '' ? '' : `: ${detail}`}`
}
