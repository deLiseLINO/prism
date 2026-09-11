import { spawn, type ChildProcessByStdio } from 'node:child_process'
import { createServer } from 'node:net'
import type { Readable } from 'node:stream'
import { DAEMON_HEALTH_PATH } from '@prism/contracts'
import type { ManagementReply } from '@prism/contracts'

export type ForwardState = 'starting' | 'ready' | 'down'

export interface SshForwardOptions {
  readonly sshPath?: string
  readonly connectTimeoutMs?: number
  readonly backoffBaseMs?: number
  readonly backoffMaxMs?: number
}

export interface SshForward {
  readonly baseUrl: string
  onState(listener: (state: ForwardState, error: string | null) => void): () => void
  call(path: string, init: RequestInit & { signal?: AbortSignal }): Promise<ManagementReply>
  stop(): void
}

export async function allocateLoopbackPort(): Promise<number> {
  const { promise, resolve, reject } = Promise.withResolvers<number>()
  const server = createServer()
  server.once('error', reject)
  server.listen(0, '127.0.0.1', () => {
    const address = server.address()
    const port = typeof address === 'object' && address !== null ? address.port : 0
    server.close(() => {
      if (port > 0) {
        resolve(port)
      } else {
        reject(new Error('prism: failed to allocate a loopback port for the ssh forward'))
      }
    })
  })
  return await promise
}

export async function spawnForward(address: string, remotePort: number, options: SshForwardOptions = {}): Promise<SshForward> {
  const sshPath = options.sshPath ?? 'ssh'
  const connectTimeoutMs = options.connectTimeoutMs ?? 15_000
  const backoffBaseMs = options.backoffBaseMs ?? 1_000
  const backoffMaxMs = options.backoffMaxMs ?? 30_000

  const localPort = await allocateLoopbackPort()
  const baseUrl = `http://127.0.0.1:${localPort}`

  let state: ForwardState = 'down'
  let lastError: string | null = null
  let child: ChildProcessByStdio<null, Readable, Readable> | null = null
  let stopped = false
  let backoff = backoffBaseMs
  const listeners = new Set<(state: ForwardState, error: string | null) => void>()

  function setState(next: ForwardState, error: string | null): void {
    state = next
    lastError = error
    for (const listener of listeners) {
      listener(next, error)
    }
  }

  function spawnChild(): void {
    if (stopped) return
    const proc = spawn(sshPath, [
      '-N',
      '-o', 'BatchMode=yes',
      '-o', 'StrictHostKeyChecking=accept-new',
      '-o', 'ExitOnForwardFailure=yes',
      '-o', 'ServerAliveInterval=15',
      '-o', 'ServerAliveCountMax=3',
      '-o', 'ConnectTimeout=10',
      '-L', `127.0.0.1:${localPort}:127.0.0.1:${remotePort}`,
      address,
    ], { stdio: ['ignore', 'pipe', 'pipe'] })

    child = proc
    let stderr = ''
    proc.stderr.setEncoding('utf8')
    proc.stderr.on('data', (chunk: string) => {
      stderr = (stderr + chunk).slice(-2000)
    })
    proc.once('exit', (code, signal) => {
      if (child === proc) child = null
      if (stopped) return
      const reason = signal !== null ? `ssh exited on signal ${signal}` : `ssh exited with code ${code}`
      setState('down', stderr.trim() !== '' ? `${reason}: ${stderr.trim()}` : reason)
      setTimeout(spawnChild, backoff)
      backoff = Math.min(backoff * 2, backoffMaxMs)
    })
  }

  function stop(): void {
    if (stopped) return
    stopped = true
    if (child !== null) {
      child.removeAllListeners('exit')
      child.kill('SIGTERM')
      child = null
    }
    setState('down', 'forward stopped')
  }

  spawnChild()
  setState('starting', null)

  const deadline = Date.now() + connectTimeoutMs
  let healthy = false
  while (Date.now() < deadline) {
    if (stopped || child === null) break
    if (await probeHealth(baseUrl, Math.min(1_000, deadline - Date.now()))) {
      healthy = true
      break
    }
    await sleep(250)
  }
  if (!healthy) {
    const reason = lastError === null || lastError === 'forward stopped' ? '' : `: ${lastError}`
    stop()
    throw new Error(`prism: ssh forward to ${address} did not become healthy within ${connectTimeoutMs}ms${reason}`)
  }
  setState('ready', null)

  return {
    baseUrl,
    onState(listener) {
      listeners.add(listener)
      listener(state, lastError)
      return () => { listeners.delete(listener) }
    },
    async call(path, init) {
      try {
        const response = await fetch(baseUrl + path, init)
        const text = await response.text()
        let body: unknown
        try {
          body = JSON.parse(text)
        } catch {
          body = text
        }
        return { ok: response.ok, status: response.status, body }
      } catch (error) {
        return { ok: false, status: 0, error: error instanceof Error ? error.message : String(error) }
      }
    },
    stop,
  }
}

async function probeHealth(baseUrl: string, timeoutMs: number): Promise<boolean> {
  try {
    const response = await fetch(baseUrl + DAEMON_HEALTH_PATH, { signal: AbortSignal.timeout(Math.max(1, timeoutMs)) })
    return response.ok
  } catch {
    return false
  }
}

function sleep(ms: number): Promise<void> {
  const { promise, resolve } = Promise.withResolvers<void>()
  setTimeout(resolve, ms)
  return promise
}
