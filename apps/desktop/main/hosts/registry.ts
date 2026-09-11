import type { HostView, ManagementCall, ManagementReply } from '@prism/contracts'
import { DAEMON_DEFAULT_PORT } from '@prism/contracts'
import type { ManagementProxy } from '../../shared/management'
import { spawnForward, type SshForward, type SshForwardOptions } from './forward'

const FORWARD_COOLDOWN_MS = 30_000

export interface HostProxyRegistryOptions extends SshForwardOptions {
  readonly fetchHosts: () => Promise<readonly HostView[]>
  readonly syncIntervalMs?: number
}

type Lane =
  | { readonly kind: 'local' }
  | { readonly kind: 'remote'; address: string; port: number; forward: SshForward | null; pending: Promise<SshForward> | null; failedUntil: number }

export class HostProxyRegistry {
  private readonly local: ManagementProxy
  private readonly options: HostProxyRegistryOptions
  private readonly lanes = new Map<string, Lane>()
  private syncTimer: ReturnType<typeof setInterval> | null = null
  private disposed = false

  constructor(local: ManagementProxy, options: HostProxyRegistryOptions) {
    this.local = local
    this.options = options
    this.lanes.set('local', { kind: 'local' })
  }

  start(): void {
    if (this.syncTimer !== null) return
    void this.sync()
    this.syncTimer = setInterval(() => { void this.sync() }, this.options.syncIntervalMs ?? 30_000)
  }

  dispose(): void {
    this.disposed = true
    if (this.syncTimer !== null) {
      clearInterval(this.syncTimer)
      this.syncTimer = null
    }
    for (const lane of this.lanes.values()) {
      if (lane.kind === 'remote') lane.forward?.stop()
    }
    this.lanes.clear()
    this.lanes.set('local', { kind: 'local' })
  }

  async route(host: string | undefined): Promise<RemoteTarget> {
    if (host === undefined || host === '' || host === 'local') {
      return { kind: 'local', call: (request) => this.local.call(this.stripHost(request)) }
    }
    let lane = this.lanes.get(host)
    if (lane === undefined) {
      await this.sync()
      lane = this.lanes.get(host)
    }
    if (lane === undefined) {
      throw new Error(`prism: unknown host ${host}; add it in Machines first`)
    }

    if (lane.kind === 'local') {
      throw new Error(`prism: host ${host} is not remote`)
    }
    const forward = await this.ensureForward(host, lane)
    return {
      kind: 'remote',
      host,
      call: (request) => forward.call(request.path, {
        method: request.method,
        headers: request.body === undefined ? undefined : { 'content-type': 'application/json' },
        body: request.body === undefined ? undefined : JSON.stringify(request.body),
      }),
    }
  }

  private ensureForward(host: string, lane: Extract<Lane, { kind: 'remote' }>): Promise<SshForward> {
    if (lane.forward !== null) return Promise.resolve(lane.forward)
    if (lane.pending !== null) return lane.pending
    if (Date.now() < lane.failedUntil) {
      return Promise.reject(new Error(`prism: ssh forward to ${host} failed recently; retrying soon`))
    }
    const pending = spawnForward(lane.address, lane.port, this.options)
      .then((forward) => {
        lane.forward = forward
        lane.pending = null
        forward.onState((state) => {
          if (state === 'down') {
            lane.forward = null
            lane.pending = null
            lane.failedUntil = Date.now() + FORWARD_COOLDOWN_MS
          }
        })
        return forward
      })
      .catch((error: unknown) => {
        lane.pending = null
        lane.failedUntil = Date.now() + FORWARD_COOLDOWN_MS
        throw error instanceof Error ? error : new Error(String(error))
      })
    lane.pending = pending
    return pending
  }

  async sync(): Promise<void> {
    if (this.disposed) return
    let hosts: readonly HostView[]
    try {
      hosts = await this.options.fetchHosts()
    } catch {
      return
    }
    const seen = new Set<string>(['local'])
    for (const host of hosts) {
      if (host.local || host.id === 'local') continue
      seen.add(host.id)
      const existing = this.lanes.get(host.id)
      if (existing !== undefined && existing.kind === 'remote') {
        existing.address = host.address ?? existing.address
        existing.port = host.daemonPort ?? existing.port
        continue
      }
      this.lanes.set(host.id, {
        kind: 'remote',
        address: host.address ?? '',
        port: host.daemonPort ?? DAEMON_DEFAULT_PORT,
        forward: null,
        pending: null,
        failedUntil: 0,
      })
    }
    for (const id of [...this.lanes.keys()]) {
      if (!seen.has(id)) {
        const lane = this.lanes.get(id)
        if (lane !== undefined && lane.kind === 'remote') lane.forward?.stop()
        this.lanes.delete(id)
      }
    }
  }

  private stripHost(request: ManagementCall): ManagementCall {
    return { method: request.method, path: request.path, ...(request.body === undefined ? {} : { body: request.body }) }
  }
}

export type RemoteTarget =
  | { readonly kind: 'local'; readonly call: (request: ManagementCall) => Promise<ManagementReply> }
  | { readonly kind: 'remote'; readonly host: string; readonly call: (request: ManagementCall) => Promise<ManagementReply> }
