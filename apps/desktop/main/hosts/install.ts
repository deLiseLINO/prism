import { spawn } from 'node:child_process'
import { createReadStream, existsSync } from 'node:fs'
import { app } from 'electron'
import path from 'node:path'
import { DAEMON_DEFAULT_PORT, DAEMON_HEALTH_PATH } from '@prism/contracts'
import type { HostInstallReply } from '@prism/contracts'
import type { ManagementProxy } from '../../shared/management'
import { spawnForward, type SshForward } from './forward'

export interface HostInstallDeps {
  readonly management: ManagementProxy
  readonly onRegistered: () => void
}

const REMOTE_DAEMON_PORT = Number.parseInt(process.env['PRISM_REMOTE_DAEMON_PORT'] ?? '', 10) || DAEMON_DEFAULT_PORT

const PROBE_TIMEOUT_MS = 15_000
const UPLOAD_TIMEOUT_MS = 300_000
const VERIFY_TIMEOUT_MS = 30_000


const SSH_BASE_FLAGS = [
  '-o', 'BatchMode=yes',
  '-o', 'StrictHostKeyChecking=accept-new',
  '-o', 'ConnectTimeout=10',
]

const inFlight = new Set<string>()


export function parseHostInstallRequest(input: unknown): { host: string } {
  if (typeof input !== 'object' || input === null) {
    throw new Error('prism: host install request must be an object with host')
  }
  const host = (input as Record<string, unknown>)['host']
  if (typeof host !== 'string' || host === '' || host === 'local') {
    throw new Error('prism: host install requires a non-empty remote host id')
  }
  return { host }
}

export async function installHostDaemon(deps: HostInstallDeps, request: { host: string }): Promise<HostInstallReply> {
  const { host } = request
  if (inFlight.has(host)) {
    return { ok: false, host, phase: 'probe', reason: 'an install is already running for this machine' }
  }
  inFlight.add(host)
  try {
    return await runInstall(deps, host)
  } finally {
    inFlight.delete(host)
  }
}

async function runInstall(deps: HostInstallDeps, host: string): Promise<HostInstallReply> {
  const address = await hostAddress(deps, host)
  if (address === null) {
    return { ok: false, host, phase: 'probe', reason: `machine ${host} is not in the host list` }
  }

  const adopted = await healthProbe(address, PROBE_TIMEOUT_MS)
  if (adopted !== null) {
    logInstall(host, 'adopt', `healthy prismd ${adopted} already running`)
    if (!(await registerExternal(deps, host, address))) {
      return { ok: false, host, phase: 'register', reason: 'prismd is running on the machine, but saving its port in the config failed; click Install again' }
    }
    deps.onRegistered()
    return { ok: true, host, version: adopted, adopted: true }
  }

  const platform = await probePlatform(address)
  if (typeof platform !== 'string') {
    return { ok: false, host, phase: 'probe', reason: platform.reason }
  }
  const binary = remoteBinaryPath(platform)
  if (binary === null || !existsSync(binary)) {
    return {
      ok: false, host, phase: 'probe',
      reason: `no prismd build for this machine's platform (${platform}); Prism bundles linux/amd64 and linux/arm64`,
    }
  }

  const home = await remoteHome(address)
  if (home === null) {
    return { ok: false, host, phase: 'upload', reason: 'could not read the remote home directory over ssh' }
  }
  const binPath = `${home}/.prism/remote/prismd`
  logInstall(host, 'upload', `${platform} binary from ${binary}`)
  const uploaded = await streamBinary(address, binary, binPath)
  if (!uploaded.ok) {
    logInstall(host, 'upload', `failed: ${uploaded.reason}`)
    return { ok: false, host, phase: 'upload', reason: uploaded.reason }
  }
  logInstall(host, 'start', `prismd on 127.0.0.1:${REMOTE_DAEMON_PORT}`)
  const started = await startRemote(address, binPath)
  if (!started.ok) {
    logInstall(host, 'start', `failed: ${started.reason}`)
    return { ok: false, host, phase: 'start', reason: started.reason }
  }
  const version = await healthProbe(address, VERIFY_TIMEOUT_MS)
  if (version === null) {
    const log = await remoteLogTail(address, home)
    const tail = log === '' ? '' : `\n${log}`
    logInstall(host, 'verify', 'prismd did not answer')
    return { ok: false, host, phase: 'verify', reason: `prismd started but did not answer within ${VERIFY_TIMEOUT_MS / 1000}s; click Install again to re-check${tail}` }
  }
  logInstall(host, 'verify', `prismd ${version} healthy`)
  if (!(await registerExternal(deps, host, address))) {
    return { ok: false, host, phase: 'register', reason: 'prismd is running on the machine, but saving its port in the config failed; click Install again' }
  }
  logInstall(host, 'register', `host flipped to its own daemon on port ${REMOTE_DAEMON_PORT}`)
  deps.onRegistered()
  return { ok: true, host, version, adopted: false }
}

function logInstall(host: string, phase: string, detail: string): void {
  console.log(`prism: install ${host} ${phase}: ${detail}`)
}


type Platform = 'linux-amd64' | 'linux-arm64' | 'darwin-same'

async function probePlatform(address: string): Promise<Platform | { reason: string }> {
  const out = await sshOutput(address, 'uname -s && uname -m', PROBE_TIMEOUT_MS)
  if (!out.ok) {
    return { reason: `ssh probe failed: ${out.reason}` }
  }
  const [os, arch] = out.stdout.split('\n')
  if (os === 'Linux' && (arch === 'x86_64' || arch === 'amd64')) return 'linux-amd64'
  if (os === 'Linux' && (arch === 'aarch64' || arch === 'arm64')) return 'linux-arm64'
  if (os === 'Darwin') {
    const localArch = process.arch === 'x64' ? 'x86_64' : process.arch === 'arm64' ? 'arm64' : ''
    if (arch === localArch) return 'darwin-same'
    return { reason: `the machine runs macOS/${arch ?? 'unknown'} while this machine is macOS/${process.arch}; copy prismd there manually and set its daemon port in the config` }
  }
  return { reason: `the machine runs ${os ?? 'unknown'}/${arch ?? 'unknown'}; Prism bundles prismd for linux and same-arch macOS` }
}

function remoteBinaryPath(platform: Platform): string | null {
  const dir = app.isPackaged
    ? path.join(process.resourcesPath, 'prismd')
    : path.join(app.getAppPath(), 'resources', 'prismd')
  if (platform === 'darwin-same') return path.join(dir, 'prismd')
  return path.join(dir, `prismd-${platform}`)
}


async function remoteHome(address: string): Promise<string | null> {
  const out = await sshOutput(address, 'printf %s "$HOME"', PROBE_TIMEOUT_MS)
  if (!out.ok || out.stdout === '') return null
  return out.stdout
}

async function healthProbe(address: string, timeoutMs: number): Promise<string | null> {
  let forward: SshForward | null = null
  try {
    forward = await spawnForward(address, REMOTE_DAEMON_PORT, { connectTimeoutMs: timeoutMs })
    const reply = await forward.call(DAEMON_HEALTH_PATH, { signal: AbortSignal.timeout(5_000) })
    if (!reply.ok || reply.status !== 200) return null
    const body = reply.body as { status?: unknown; version?: unknown } | undefined
    if (typeof body !== 'object' || body === null || body.status !== 'ok' || typeof body.version !== 'string') return null
    return body.version
  } catch {
    return null
  } finally {
    forward?.stop()
  }
}

async function streamBinary(address: string, srcPath: string, remotePath: string): Promise<{ ok: true } | { ok: false; reason: string }> {
  const script = `mkdir -p '${dirOf(remotePath)}' && cat > '${remotePath}.new' && chmod 755 '${remotePath}.new' && mv '${remotePath}.new' '${remotePath}'`
  const child = spawn('ssh', [...SSH_BASE_FLAGS, address, script], { stdio: ['pipe', 'pipe', 'pipe'] })
  let stderr = ''
  child.stderr.setEncoding('utf8')
  child.stderr.on('data', (chunk: string) => { stderr = (stderr + chunk).slice(-2000) })
  const done = new Promise<{ code: number | null; signal: string | null }>((resolve) => {
    child.once('exit', (code, signal) => resolve({ code, signal }))
  })
  const timer = setTimeout(() => child.kill('SIGTERM'), UPLOAD_TIMEOUT_MS)
  try {
    await new Promise<void>((resolve, reject) => {
      const stream = createReadStream(srcPath)
      stream.on('error', reject)
      stream.pipe(child.stdin)
      child.stdin.on('error', reject)
      child.stdin.on('close', resolve)
    })
  } catch (error) {
    child.kill('SIGTERM')
    return { ok: false, reason: `binary upload failed: ${error instanceof Error ? error.message : String(error)}` }
  }
  const exit = await done
  clearTimeout(timer)
  if (exit.code !== 0) {
    const reason = exit.signal !== null ? `ssh exited on signal ${exit.signal}` : `ssh exited with code ${exit.code}`
    return { ok: false, reason: stderr.trim() !== '' ? `${reason}: ${stderr.trim()}` : reason }
  }
  return { ok: true }
}

async function startRemote(address: string, binPath: string): Promise<{ ok: true } | { ok: false; reason: string }> {
  const logPath = `${dirOf(binPath)}/prismd.log`
  const script = `nohup '${binPath}' --listen 127.0.0.1:${REMOTE_DAEMON_PORT} >> '${logPath}' 2>&1 < /dev/null & exit 0`
  const out = await sshOutput(address, script, PROBE_TIMEOUT_MS)
  if (!out.ok) {
    return { ok: false, reason: `starting prismd failed: ${out.reason}` }
  }
  return { ok: true }
}

async function remoteLogTail(address: string, home: string): Promise<string> {
  const out = await sshOutput(address, `tail -c 2000 '${home}/.prism/remote/prismd.log' 2>/dev/null`, PROBE_TIMEOUT_MS)
  return out.ok ? out.stdout.trim() : ''
}

async function hostAddress(deps: HostInstallDeps, host: string): Promise<string | null> {
  const reply = await deps.management.call({ method: 'GET', path: '/api/v1/hosts' })
  if (!reply.ok || reply.status !== 200) return null
  const body = reply.body as { hosts?: { id?: unknown; address?: unknown }[] } | undefined
  if (typeof body !== 'object' || body === null || !Array.isArray(body.hosts)) return null
  const entry = body.hosts.find((h) => h?.id === host)
  return typeof entry?.address === 'string' && entry.address !== '' ? entry.address : null
}

async function registerExternal(deps: HostInstallDeps, host: string, address: string): Promise<boolean> {
  const gen = await currentGeneration(deps)
  if (gen === null) return false
  const reply = await deps.management.call({
    method: 'PUT',
    path: `/api/v1/hosts/${encodeURIComponent(host)}`,
    body: { address, daemonPort: REMOTE_DAEMON_PORT, expectedGeneration: gen },
  })
  return reply.ok && reply.status === 200
}

async function currentGeneration(deps: HostInstallDeps): Promise<number | null> {
  const reply = await deps.management.call({ method: 'GET', path: '/api/v1/providers' })
  if (!reply.ok || reply.status !== 200) return null
  const body = reply.body as { generation?: unknown } | undefined
  if (typeof body !== 'object' || body === null || typeof body.generation !== 'number') return null
  return body.generation
}

async function sshOutput(address: string, script: string, timeoutMs: number): Promise<{ ok: true; stdout: string } | { ok: false; reason: string }> {
  const child = spawn('ssh', [...SSH_BASE_FLAGS, address, script], { stdio: ['ignore', 'pipe', 'pipe'] })
  let stdout = ''
  let stderr = ''
  child.stdout.setEncoding('utf8')
  child.stderr.setEncoding('utf8')
  child.stdout.on('data', (chunk: string) => { stdout += chunk })
  child.stderr.on('data', (chunk: string) => { stderr = (stderr + chunk).slice(-2000) })
  const timer = setTimeout(() => child.kill('SIGTERM'), timeoutMs)
  const exit = await new Promise<{ code: number | null; signal: string | null }>((resolve) => {
    child.once('exit', (code, signal) => resolve({ code, signal }))
  })
  clearTimeout(timer)
  if (exit.code !== 0) {
    const reason = exit.signal !== null ? `ssh exited on signal ${exit.signal}` : `ssh exited with code ${exit.code}`
    return { ok: false, reason: stderr.trim() !== '' ? `${reason}: ${stderr.trim()}` : reason }
  }
  return { ok: true, stdout }
}

function dirOf(p: string): string {
  const idx = p.lastIndexOf('/')
  return idx <= 0 ? '.' : p.slice(0, idx)
}
