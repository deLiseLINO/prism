import { describe, expect, it, vi } from 'vitest'

vi.mock('electron', () => ({ app: { isPackaged: false, getAppPath: () => '/app' } }))

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
