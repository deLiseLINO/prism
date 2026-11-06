import { spawnSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, readlinkSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { REMOTE_INSTALL_SCRIPT } from '../main/hosts/remote-script'

// The script runs against a fake machine: a private HOME, a PATH without
// systemd or launchd, a curl that serves releases from a directory, and a
// prism stand-in whose daemon publishes its health to $HOME/health.json.
const REAL_TOOLS = ['sh', 'mkdir', 'cat', 'rm', 'sleep', 'sha256sum', 'cut', 'awk', 'tar', 'gzip', 'cp', 'chmod', 'mv', 'readlink', 'ln', 'id', 'pgrep', 'sed', 'dirname', 'nohup', 'printf', 'tail']
const BASE_URL = 'http://release.test'
const PORT = '47391'

let machine: string
let home: string
let releases: string

function which(tool: string): string {
  const found = spawnSync('sh', ['-c', `command -v ${tool}`], { encoding: 'utf8' }).stdout.trim()
  if (found === '') throw new Error(`test needs ${tool}`)
  return found
}

function writeTool(dir: string, name: string, body: string): void {
  writeFileSync(path.join(dir, name), `#!/bin/sh\n${body}`)
  chmodSync(path.join(dir, name), 0o755)
}

function fakePrism(version: string): string {
  return `case "$1" in
  version) echo "prism ${version}" ;;
  daemon)
    trap 'rm -f "$HOME/health.json"; exit 0' TERM
    printf '{"status":"ok","version":"${version}"}' > "$HOME/health.json"
    while :; do sleep 0.1; done ;;
esac
`
}

// publish puts a release for version on the fake download server.
function publish(version: string, opts: { corruptSum?: boolean } = {}): void {
  const build = mkdtempSync(path.join(machine, 'build-'))
  writeTool(build, 'prism', fakePrism(version))
  const dir = path.join(releases, `v${version}`)
  mkdirSync(dir, { recursive: true })
  const name = `prism_${version}_linux_amd64.tar.gz`
  spawnSync('tar', ['-czf', path.join(dir, name), '-C', build, 'prism'])
  const sum = opts.corruptSum === true ? '0'.repeat(64) : createHash('sha256').update(readFileSync(path.join(dir, name))).digest('hex')
  writeFileSync(path.join(dir, 'SHA256SUMS'), `${sum}  ${name}\n`)
}

function install(version: string, extra: string[] = [], env: Record<string, string> = {}): { code: number | null; out: Map<string, string>; stderr: string } {
  const result = spawnSync('sh', ['-s', '--', '--version', version, '--port', PORT, '--base-url', BASE_URL, ...extra], {
    input: REMOTE_INSTALL_SCRIPT,
    cwd: home,
    env: { HOME: home, PATH: path.join(machine, 'bin'), FAKE_RELEASES: releases, ...env },
    encoding: 'utf8',
    timeout: 30_000,
  })
  const out = new Map<string, string>()
  for (const line of result.stdout.split('\n')) {
    const eq = line.indexOf('=')
    if (eq > 0) out.set(line.slice(0, eq), line.slice(eq + 1))
  }
  return { code: result.status, out, stderr: result.stderr }
}

function daemonPids(): string[] {
  const res = spawnSync('pgrep', ['-f', `${home}/.prism/remote/[^ ]*prism daemon --listen 127.0.0.1:${PORT}`], { encoding: 'utf8' })
  return res.stdout.split('\n').filter(Boolean)
}

function runningVersion(): string | null {
  const file = path.join(home, 'health.json')
  return existsSync(file) ? (JSON.parse(readFileSync(file, 'utf8')) as { version: string }).version : null
}

// The daemon is a real detached process, so fake timers cannot move it; this
// polls its health file and returns as soon as the condition holds.
async function waitFor(check: () => boolean): Promise<void> {
  for (let i = 0; i < 50 && !check(); i++) await new Promise((r) => setTimeout(r, 100))
}

beforeEach(() => {
  machine = mkdtempSync(path.join(tmpdir(), 'prism-remote-'))
  home = path.join(machine, 'home')
  releases = path.join(machine, 'releases')
  const bin = path.join(machine, 'bin')
  mkdirSync(home)
  mkdirSync(releases)
  mkdirSync(bin)
  for (const tool of REAL_TOOLS) symlinkSync(which(tool), path.join(bin, tool))
  writeTool(bin, 'uname', `case "$1" in -s) echo Linux ;; -m) echo x86_64 ;; esac\n`)
  writeTool(bin, 'curl', `out='' url=''
while [ $# -gt 0 ]; do
  case "$1" in -o) out=$2; shift 2 ;; --*-time|--connect-timeout) shift 2 ;; -*) shift ;; *) url=$1; shift ;; esac
done
case "$url" in
  */api/v1/health) exec cat "$HOME/health.json" 2>/dev/null ;;
esac
[ -n "\${FAKE_OFFLINE:-}" ] && exit 6
file="$FAKE_RELEASES/\${url#${BASE_URL}/}"
[ -f "$file" ] || exit 22
cp "$file" "$out"
`)
})

afterEach(() => {
  spawnSync('pkill', ['-f', `${home}/`])
  rmSync(machine, { recursive: true, force: true })
})

describe('remote install script', () => {
  it('downloads the release, checks it and starts prism without a service manager', async () => {
    publish('1.0.0')
    const run = install('1.0.0')
    expect(run.stderr).toBe('')
    expect(run.code).toBe(0)
    expect(run.out.get('target')).toBe('linux_amd64')
    expect(run.out.get('state')).toBe('installed')
    expect(run.out.get('service')).toBe('none')
    expect(readlinkSync(path.join(home, '.prism/remote/current'))).toBe(path.join(home, '.prism/remote/versions/1.0.0'))
    await waitFor(() => runningVersion() !== null)
    expect(runningVersion()).toBe('1.0.0')
  })

  it('leaves a running prism on the same version alone', async () => {
    publish('1.0.0')
    install('1.0.0')
    await waitFor(() => runningVersion() !== null)
    const before = daemonPids()
    const again = install('1.0.0', [], { FAKE_OFFLINE: '1' })
    expect(again.code).toBe(0)
    expect(again.out.get('state')).toBe('current')
    expect(daemonPids()).toEqual(before)
  })

  it('starts a stopped prism again on the installed version without downloading', async () => {
    publish('1.0.0')
    install('1.0.0')
    await waitFor(() => runningVersion() !== null)
    spawnSync('pkill', ['-f', `${home}/`])
    await waitFor(() => runningVersion() === null)
    const again = install('1.0.0', [], { FAKE_OFFLINE: '1' })
    expect(again.code).toBe(0)
    await waitFor(() => runningVersion() !== null)
    expect(runningVersion()).toBe('1.0.0')
  })

  it('moves to a new version, stops the old daemon and keeps one previous version', async () => {
    for (const v of ['1.0.0', '1.1.0', '1.2.0']) publish(v)
    install('1.0.0')
    install('1.1.0')
    await waitFor(() => runningVersion() === '1.1.0')
    const run = install('1.2.0')
    expect(run.out.get('state')).toBe('updated')
    await waitFor(() => runningVersion() === '1.2.0')
    expect(runningVersion()).toBe('1.2.0')
    expect(daemonPids()).toHaveLength(1)
    const versions = spawnSync('ls', [path.join(home, '.prism/remote/versions')], { encoding: 'utf8' }).stdout.split('\n').filter(Boolean)
    expect(versions).toEqual(['1.1.0', '1.2.0'])
  })

  it('refuses an archive whose checksum does not match and keeps the running version', async () => {
    publish('1.0.0')
    install('1.0.0')
    await waitFor(() => runningVersion() !== null)
    publish('2.0.0', { corruptSum: true })
    const run = install('2.0.0')
    expect(run.code).toBe(12)
    expect(run.stderr).toContain('checksum mismatch')
    expect(readlinkSync(path.join(home, '.prism/remote/current'))).toBe(path.join(home, '.prism/remote/versions/1.0.0'))
    expect(runningVersion()).toBe('1.0.0')
  })

  it('asks for the archive when the machine cannot download it, then installs the upload', async () => {
    publish('1.0.0')
    const offline = install('1.0.0', [], { FAKE_OFFLINE: '1' })
    expect(offline.code).toBe(20)
    expect(offline.out.get('need')).toBe('archive')
    expect(offline.out.get('target')).toBe('linux_amd64')

    const name = 'prism_1.0.0_linux_amd64.tar.gz'
    mkdirSync(path.join(home, '.prism/remote/incoming'), { recursive: true })
    writeFileSync(path.join(home, '.prism/remote/incoming', name), readFileSync(path.join(releases, 'v1.0.0', name)))
    const sha = readFileSync(path.join(releases, 'v1.0.0/SHA256SUMS'), 'utf8').split(' ')[0] ?? ''
    const uploaded = install('1.0.0', ['--archive', `.prism/remote/incoming/${name}`, '--sha256', sha], { FAKE_OFFLINE: '1' })
    expect(uploaded.code).toBe(0)
    expect(uploaded.out.get('state')).toBe('installed')
    await waitFor(() => runningVersion() !== null)
    expect(runningVersion()).toBe('1.0.0')
    expect(existsSync(path.join(home, '.prism/remote/incoming'))).toBe(false)
  })

  it('keeps a prism it did not start and reports its version', () => {
    publish('1.0.0')
    writeFileSync(path.join(home, 'health.json'), '{"status":"ok","version":"0.9.0"}')
    const run = install('1.0.0')
    expect(run.code).toBe(0)
    expect(run.out.get('state')).toBe('adopted')
    expect(run.out.get('version')).toBe('0.9.0')
    expect(daemonPids()).toEqual([])
  })

  it('stops the daemon an older install left at the legacy path', async () => {
    const legacy = path.join(home, '.prism/remote/prism')
    mkdirSync(path.dirname(legacy), { recursive: true })
    writeTool(path.dirname(legacy), 'prism', fakePrism('0.5.0'))
    spawnSync('sh', ['-c', `nohup '${legacy}' daemon --listen 127.0.0.1:${PORT} >/dev/null 2>&1 &`], { env: { HOME: home, PATH: path.join(machine, 'bin') } })
    await waitFor(() => runningVersion() === '0.5.0')
    publish('1.0.0')
    const run = install('1.0.0')
    expect(run.code).toBe(0)
    await waitFor(() => runningVersion() === '1.0.0')
    expect(runningVersion()).toBe('1.0.0')
    expect(existsSync(legacy)).toBe(false)
    expect(daemonPids()).toHaveLength(1)
  })
})
