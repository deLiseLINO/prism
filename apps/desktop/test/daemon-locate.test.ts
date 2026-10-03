import { describe, expect, it, vi } from 'vitest'

const runCliMock = vi.hoisted(() => vi.fn())
let userData = ''

vi.mock('electron', () => ({ app: { isPackaged: false, getAppPath: () => '/app', getPath: () => userData } }))
vi.mock('../main/daemon/cli', () => ({ runCli: runCliMock }))

describe('bundledDaemonBinary', () => {
  it('maps every supported platform/arch to the bundled prism binary name', async () => {
    const { bundledDaemonBinary } = await import('../main/daemon/locate')
    const { daemonTargets } = await import('../daemon-targets.mjs')
    const cases: Array<[NodeJS.Platform, string, string]> = [
      ['darwin', 'arm64', 'prism-darwin-arm64'],
      ['darwin', 'x64', 'prism-darwin-amd64'],
      ['linux', 'x64', 'prism-linux-amd64'],
      ['linux', 'arm64', 'prism-linux-arm64'],
      ['win32', 'x64', 'prism-windows-amd64.exe'],
      ['win32', 'arm64', 'prism-windows-arm64.exe'],
    ]
    for (const [platform, arch, name] of cases) {
      expect(bundledDaemonBinary(platform, arch)).toBe(name)
    }
    expect(daemonTargets.map((target: { name: string }) => target.name).sort()).toEqual(cases.map(([, , name]) => name).sort())
  })
})

describe('stageDaemon', () => {
  it('copies the bundled binary once per version and reuses it', async () => {
    const fs = await import('node:fs/promises')
    const os = await import('node:os')
    const path = await import('node:path')
    const root = await fs.mkdtemp(path.join(os.tmpdir(), 'prism-stage-'))
    const source = path.join(root, 'bundled')
    await fs.writeFile(source, 'binary-v1')
    userData = path.join(root, 'data')
    runCliMock.mockReset().mockResolvedValue({ code: 0, stdout: '1.2.3\n', stderr: '' })
    const { stageDaemon } = await import('../main/daemon/locate')
    const first = await stageDaemon({ path: source, source: 'bundled' })
    expect(first).toBe(path.join(userData, 'cli', '1.2.3', process.platform === 'win32' ? 'prism.exe' : 'prism'))
    expect(await fs.readFile(first, 'utf8')).toBe('binary-v1')
    if (process.platform !== 'win32') expect((await fs.stat(first)).mode & 0o777).toBe(0o755)
    await fs.writeFile(source, 'binary-v1-changed')
    const second = await stageDaemon({ path: source, source: 'bundled' })
    expect(second).toBe(first)
    expect(await fs.readFile(second, 'utf8')).toBe('binary-v1')
    expect(await fs.readdir(path.dirname(first))).toHaveLength(1)
    expect(runCliMock).toHaveBeenCalledWith(source, ['version'], expect.any(Number))
  })

  it('stages a new copy when the version changes', async () => {
    const fs = await import('node:fs/promises')
    const os = await import('node:os')
    const path = await import('node:path')
    const root = await fs.mkdtemp(path.join(os.tmpdir(), 'prism-stage-'))
    const source = path.join(root, 'bundled')
    await fs.writeFile(source, 'binary-v2')
    userData = path.join(root, 'data')
    runCliMock.mockReset().mockResolvedValue({ code: 0, stdout: '2.0.0\n', stderr: '' })
    const { stageDaemon } = await import('../main/daemon/locate')
    const staged = await stageDaemon({ path: source, source: 'bundled' })
    expect(staged).toContain(path.join('cli', '2.0.0'))
  })

  it('uses a configured binary as is', async () => {
    const { stageDaemon } = await import('../main/daemon/locate')
    expect(await stageDaemon({ path: '/custom/prism', source: 'configured' })).toBe('/custom/prism')
  })

  it('rejects when the version cannot be read', async () => {
    runCliMock.mockReset().mockResolvedValue({ code: 1, stdout: '', stderr: 'boom' })
    const { stageDaemon } = await import('../main/daemon/locate')
    await expect(stageDaemon({ path: '/x/prism', source: 'bundled' })).rejects.toThrow('boom')
  })
})
