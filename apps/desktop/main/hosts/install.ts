import { spawn, type ChildProcess } from 'node:child_process'
import { createHash } from 'node:crypto'
import { readFile } from 'node:fs/promises'
import { DAEMON_DEFAULT_PORT, DAEMON_HEALTH_PATH } from '@prism/contracts'
import type { HostInstallReply, HostInstallService } from '@prism/contracts'
import type { ManagementProxy } from '../../shared/management'
import { spawnForward, type SshForward } from './forward'
import { REMOTE_INSTALL_SCRIPT } from './remote-script'

// A remote machine's OS and processor in release archive terms, e.g. linux_arm64.
export type RemoteTarget = `${'linux' | 'darwin'}_${'amd64' | 'arm64'}`

export interface HostInstallDeps {
  readonly management: ManagementProxy
  readonly onRegistered: () => void
  // The prism version to run on remote machines: the version of this app.
  readonly version: string
  // Where release archives live: <base>/v<version>/prism_<version>_<target>.tar.gz.
  readonly releaseBaseUrl: string
  // A prism binary this app carries for target, used when no release exists
  // for this version (development builds); null when it carries none.
  readonly bundledBinary: (target: RemoteTarget) => string | null
}

const REMOTE_DAEMON_PORT = Number.parseInt(process.env['PRISM_REMOTE_DAEMON_PORT'] ?? '', 10) || DAEMON_DEFAULT_PORT

const SCRIPT_TIMEOUT_MS = 360_000
const UPLOAD_TIMEOUT_MS = 300_000
const FETCH_TIMEOUT_MS = 300_000
const PROBE_TIMEOUT_MS = 15_000
const VERIFY_TIMEOUT_MS = 30_000
const NEED_ARCHIVE_EXIT = 20
const INCOMING_DIR = '.prism/remote/incoming'

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

// installHostDaemon installs, updates, or restarts prism on one remote machine
// so it runs this app's version. It is safe to repeat: a machine already on
// this version with its service in place is left running.
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

// maintainHostDaemons runs the install on every machine that already has
// prism, so after this app updates each machine moves to the same version and
// a stopped prism without a service starts again.
export async function maintainHostDaemons(deps: HostInstallDeps): Promise<void> {
  const reply = await deps.management.call({ method: 'GET', path: '/api/v1/hosts' })
  if (!reply.ok || reply.status !== 200) return
  const body = reply.body as { hosts?: { id?: unknown; local?: unknown; daemonPort?: unknown }[] } | undefined
  const installed = (body?.hosts ?? []).filter((h) => h.local !== true && typeof h.id === 'string' && typeof h.daemonPort === 'number')
  await Promise.all(installed.map(async (h) => {
    const result = await installHostDaemon(deps, { host: h.id as string })
    if (!result.ok) console.error(`prism: keeping ${result.host} up to date failed at ${result.phase}: ${result.reason}`)
  }))
}

async function runInstall(deps: HostInstallDeps, host: string): Promise<HostInstallReply> {
  const entry = await hostEntry(deps, host)
  if (entry === null) {
    return { ok: false, host, phase: 'probe', reason: `machine ${host} is not in the host list` }
  }
  const { address } = entry
  const baseArgs = ['--version', deps.version, '--port', String(REMOTE_DAEMON_PORT), '--base-url', deps.releaseBaseUrl]
  logInstall(host, 'probe', `prism ${deps.version} on ${address}`)
  let run = await runScript(address, baseArgs)
  if (!run.ok && run.code === NEED_ARCHIVE_EXIT) {
    const target = parseTarget(run.lines)
    if (target === null) {
      return { ok: false, host, phase: 'probe', reason: 'the machine did not report its platform' }
    }
    logInstall(host, 'upload', `${address} cannot download prism; sending it from this machine`)
    const upload = await uploadPrism(deps, address, target)
    if (!upload.ok) return { ok: false, host, phase: 'upload', reason: upload.reason }
    run = await runScript(address, [...baseArgs, ...upload.args])
  }
  if (!run.ok) {
    logInstall(host, 'start', `failed: ${run.reason}`)
    return { ok: false, host, phase: 'start', reason: run.reason }
  }
  const state = run.lines.get('state') ?? ''
  const version = run.lines.get('version') ?? ''
  if (state === 'adopted') {
    logInstall(host, 'adopt', `prism ${version} already runs there and was not started by this app`)
    if (!(await registerExternal(deps, host, entry))) {
      return { ok: false, host, phase: 'register', reason: 'prism runs on the machine, but saving its port in the config failed; click Install again' }
    }
    deps.onRegistered()
    const notice = version === deps.version
      ? null
      : `prism ${version} already runs on this machine and was not started by Prism, so it stays as is. Update it there to ${deps.version}.`
    return { ok: true, host, version, adopted: true, service: null, notice }
  }
  const rawService = run.lines.get('service')
  const service: HostInstallService = rawService === 'systemd' || rawService === 'launchd' ? rawService : 'none'
  logInstall(host, 'verify', `${state}, service ${service}`)
  const healthy = await healthProbe(address, VERIFY_TIMEOUT_MS)
  if (healthy === null) {
    const log = await remoteLogTail(address)
    const tail = log === '' ? '' : `\n${log}`
    return { ok: false, host, phase: 'verify', reason: `prism started but did not answer within ${VERIFY_TIMEOUT_MS / 1000}s; click Install again to re-check${tail}` }
  }
  if (!(await registerExternal(deps, host, entry))) {
    return { ok: false, host, phase: 'register', reason: 'prism runs on the machine, but saving its port in the config failed; click Install again' }
  }
  logInstall(host, 'register', `prism ${healthy} on port ${REMOTE_DAEMON_PORT}`)
  deps.onRegistered()
  return { ok: true, host, version: healthy, adopted: false, service, notice: serviceNotice(service, run.lines.get('linger')) }
}

function serviceNotice(service: HostInstallService, linger: string | undefined): string | null {
  if (service === 'none') {
    return 'This machine has no systemd user session or launchd, so prism does not start by itself after a reboot. Prism starts it again the next time this app starts.'
  }
  if (service === 'systemd' && linger !== 'yes') {
    return 'prism starts after a reboot only once you sign in on that machine. To start it at boot, run sudo loginctl enable-linger $USER there.'
  }
  return null
}

// processExit waits for an ssh child; a spawn failure counts as an exit
// without a code so callers report it like any failed ssh run.
async function processExit(child: ChildProcess): Promise<{ code: number | null; signal: string | null }> {
  const { promise, resolve } = Promise.withResolvers<{ code: number | null; signal: string | null }>()
  child.once('exit', (code, signal) => resolve({ code, signal }))
  child.once('error', () => resolve({ code: null, signal: null }))
  return promise
}

export function parseTarget(lines: ReadonlyMap<string, string>): RemoteTarget | null {
  const raw = lines.get('target')
  return raw === 'linux_amd64' || raw === 'linux_arm64' || raw === 'darwin_amd64' || raw === 'darwin_arm64' ? raw : null
}

function logInstall(host: string, phase: string, detail: string): void {
  console.log(`prism: install ${host} ${phase}: ${detail}`)
}

// uploadPrism puts prism for target on the machine when it cannot download
// the release: the release archive fetched here first, else the binary this
// app carries. It returns the script arguments that point at the upload.
async function uploadPrism(deps: HostInstallDeps, address: string, target: RemoteTarget): Promise<{ ok: true; args: string[] } | { ok: false; reason: string }> {
  const archive = await fetchReleaseArchive(deps.releaseBaseUrl, deps.version, target)
  if (archive.ok) {
    const remote = `${INCOMING_DIR}/${archive.name}`
    const sent = await upload(address, archive.bytes, remote)
    if (!sent.ok) return sent
    return { ok: true, args: ['--archive', remote, '--sha256', archive.sha256] }
  }
  const bundled = deps.bundledBinary(target)
  if (bundled === null) {
    return { ok: false, reason: `the machine cannot download prism ${deps.version} for ${target}, and this machine could not either: ${archive.reason}` }
  }
  const remote = `${INCOMING_DIR}/prism`
  const sent = await upload(address, await readFile(bundled), remote)
  if (!sent.ok) return sent
  return { ok: true, args: ['--binary', remote] }
}

export async function fetchReleaseArchive(baseUrl: string, version: string, target: RemoteTarget): Promise<
  { ok: true; name: string; bytes: Buffer; sha256: string } | { ok: false; reason: string }
> {
  const name = `prism_${version}_${target}.tar.gz`
  try {
    const sums = await fetch(`${baseUrl}/v${version}/SHA256SUMS`, { signal: AbortSignal.timeout(FETCH_TIMEOUT_MS) })
    if (!sums.ok) return { ok: false, reason: `the prism ${version} release is not available (HTTP ${sums.status})` }
    const want = (await sums.text()).split('\n')
      .map((line) => line.trim().split(/\s+/))
      .find(([, file]) => file === name || file === `*${name}`)?.[0]
    if (want === undefined) return { ok: false, reason: `the prism ${version} release has no build for ${target}` }
    const res = await fetch(`${baseUrl}/v${version}/${name}`, { signal: AbortSignal.timeout(FETCH_TIMEOUT_MS) })
    if (!res.ok) return { ok: false, reason: `downloading ${name} failed (HTTP ${res.status})` }
    const bytes = Buffer.from(await res.arrayBuffer())
    const got = createHash('sha256').update(bytes).digest('hex')
    if (got !== want) return { ok: false, reason: `checksum mismatch for ${name}: expected ${want}, got ${got}` }
    return { ok: true, name, bytes, sha256: want }
  } catch (error) {
    return { ok: false, reason: `downloading prism ${version} failed: ${error instanceof Error ? error.message : String(error)}` }
  }
}

interface ScriptResult {
  readonly ok: boolean
  readonly code: number | null
  readonly lines: ReadonlyMap<string, string>
  readonly reason: string
}

// runScript pipes the install script to the machine's sh. Paths in args are
// relative to the remote home, where an ssh command starts.
async function runScript(address: string, args: readonly string[]): Promise<ScriptResult> {
  const command = ['sh', '-s', '--', ...args].map(shellQuote).join(' ')
  const child = spawn('ssh', [...SSH_BASE_FLAGS, address, command], { stdio: ['pipe', 'pipe', 'pipe'] })
  let stdout = ''
  let stderr = ''
  child.stdout.setEncoding('utf8')
  child.stderr.setEncoding('utf8')
  child.stdout.on('data', (chunk: string) => { stdout += chunk })
  child.stderr.on('data', (chunk: string) => { stderr = (stderr + chunk).slice(-4000) })
  child.stdin.on('error', () => {})
  child.stdin.end(REMOTE_INSTALL_SCRIPT)
  const timer = setTimeout(() => child.kill('SIGTERM'), SCRIPT_TIMEOUT_MS)
  const exit = await processExit(child)
  clearTimeout(timer)
  const lines = new Map<string, string>()
  for (const line of stdout.split('\n')) {
    const eq = line.indexOf('=')
    if (eq > 0) lines.set(line.slice(0, eq).trim(), line.slice(eq + 1).trim())
  }
  if (exit.code === 0) return { ok: true, code: 0, lines, reason: '' }
  return { ok: false, code: exit.code, lines, reason: scriptFailure(exit, stderr) }
}

function scriptFailure(exit: { code: number | null; signal: string | null }, stderr: string): string {
  const message = stderr.split('\n').map((line) => line.trim()).filter((line) => line.startsWith('prism-remote: ')).at(-1)
  if (message !== undefined) return message.slice('prism-remote: '.length)
  if (/is not recognized as an internal or external command|The system cannot find the path/i.test(stderr)) {
    return 'this machine runs Windows; prism can be installed on macOS and Linux machines for now'
  }
  const reason = exit.signal !== null ? `ssh exited on signal ${exit.signal}` : `ssh exited with code ${exit.code}`
  return stderr.trim() !== '' ? `${reason}: ${stderr.trim()}` : reason
}

function shellQuote(value: string): string {
  return `'${value.replace(/'/g, `'\\''`)}'`
}

async function upload(address: string, bytes: Buffer, remotePath: string): Promise<{ ok: true } | { ok: false; reason: string }> {
  const dir = remotePath.slice(0, remotePath.lastIndexOf('/'))
  const command = `mkdir -p ${shellQuote(dir)} && cat > ${shellQuote(remotePath)}`
  const child = spawn('ssh', [...SSH_BASE_FLAGS, address, command], { stdio: ['pipe', 'ignore', 'pipe'] })
  let stderr = ''
  child.stderr.setEncoding('utf8')
  child.stderr.on('data', (chunk: string) => { stderr = (stderr + chunk).slice(-2000) })
  child.stdin.on('error', () => {})
  child.stdin.end(bytes)
  const timer = setTimeout(() => child.kill('SIGTERM'), UPLOAD_TIMEOUT_MS)
  const exit = await processExit(child)
  clearTimeout(timer)
  if (exit.code === 0) return { ok: true }
  const reason = exit.signal !== null ? `ssh exited on signal ${exit.signal}` : `ssh exited with code ${exit.code}`
  return { ok: false, reason: `uploading prism failed: ${stderr.trim() !== '' ? `${reason}: ${stderr.trim()}` : reason}` }
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

async function remoteLogTail(address: string): Promise<string> {
  const child = spawn('ssh', [...SSH_BASE_FLAGS, address, 'tail -c 2000 .prism/remote/prism.log 2>/dev/null'], { stdio: ['ignore', 'pipe', 'ignore'] })
  let stdout = ''
  child.stdout.setEncoding('utf8')
  child.stdout.on('data', (chunk: string) => { stdout += chunk })
  const timer = setTimeout(() => child.kill('SIGTERM'), PROBE_TIMEOUT_MS)
  await processExit(child)
  clearTimeout(timer)
  return stdout.trim()
}

interface HostEntry {
  readonly address: string
  readonly daemonPort: number | null
}

async function hostEntry(deps: HostInstallDeps, host: string): Promise<HostEntry | null> {
  const reply = await deps.management.call({ method: 'GET', path: '/api/v1/hosts' })
  if (!reply.ok || reply.status !== 200) return null
  const body = reply.body as { hosts?: { id?: unknown; address?: unknown; daemonPort?: unknown }[] } | undefined
  if (typeof body !== 'object' || body === null || !Array.isArray(body.hosts)) return null
  const entry = body.hosts.find((h) => h?.id === host)
  if (typeof entry?.address !== 'string' || entry.address === '') return null
  return { address: entry.address, daemonPort: typeof entry.daemonPort === 'number' ? entry.daemonPort : null }
}

// registerExternal records the machine's daemon port. A port already recorded
// writes nothing, so repeated maintenance runs do not churn the config.
async function registerExternal(deps: HostInstallDeps, host: string, entry: HostEntry): Promise<boolean> {
  if (entry.daemonPort === REMOTE_DAEMON_PORT) return true
  const gen = await currentGeneration(deps)
  if (gen === null) return false
  const reply = await deps.management.call({
    method: 'PUT',
    path: `/api/v1/hosts/${encodeURIComponent(host)}`,
    body: { address: entry.address, daemonPort: REMOTE_DAEMON_PORT, expectedGeneration: gen },
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
