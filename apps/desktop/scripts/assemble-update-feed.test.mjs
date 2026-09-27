import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { spawnSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { parse, stringify } from 'yaml'
import { describe, expect, it } from 'vitest'

const script = fileURLToPath(new URL('./assemble-update-feed.mjs', import.meta.url))

function file(url, sha512) {
  return { url, sha512, size: url.length }
}

describe('assemble-update-feed', () => {
  it('merges macOS channels built in separate architecture jobs', async () => {
    const root = await mkdtemp(path.join(tmpdir(), 'prism-feed-'))
    try {
      const source = path.join(root, 'source')
      const destination = path.join(root, 'feed')
      await mkdir(source)
      const version = '0.1.0-rc.4'
      const channels = {
        'latest.yml': { version, files: [file('Prism-0.1.0-rc.4-setup.exe', 'win')] },
        'latest-mac-arm64.yml': { version, releaseDate: '2026-09-27T16:00:00.000Z', files: [file('Prism-0.1.0-rc.4-arm64.zip', 'arm'), file('Prism-0.1.0-rc.4-arm64.dmg', 'arm-dmg')] },
        'latest-mac-x64.yml': { version, releaseDate: '2026-09-27T16:01:00.000Z', files: [file('Prism-0.1.0-rc.4-x64.zip', 'x64'), file('Prism-0.1.0-rc.4-x64.dmg', 'x64-dmg')] },
        'latest-linux.yml': { version, files: [file('Prism-0.1.0-rc.4-x86_64.AppImage', 'linux')] },
        'latest-linux-arm64.yml': { version, files: [file('Prism-0.1.0-rc.4-arm64.AppImage', 'linux-arm')] },
      }
      await Promise.all(Object.entries(channels).map(([name, info]) => writeFile(path.join(source, name), stringify(info))))
      for (const name of ['Prism-0.1.0-rc.4-setup.exe', 'Prism-0.1.0-rc.4-arm64.zip', 'Prism-0.1.0-rc.4-arm64.dmg', 'Prism-0.1.0-rc.4-x64.zip', 'Prism-0.1.0-rc.4-x64.dmg', 'Prism-0.1.0-rc.4-x86_64.AppImage', 'Prism-0.1.0-rc.4-arm64.AppImage']) {
        await writeFile(path.join(source, name), name)
      }
      const result = spawnSync(process.execPath, [script, source, destination, version], { encoding: 'utf8' })
      expect(result.status, result.stderr).toBe(0)
      const mac = parse(await readFile(path.join(destination, 'rc-mac.yml'), 'utf8'))
      expect(mac.files.map(entry => entry.url).sort()).toEqual([
        'Prism-0.1.0-rc.4-arm64.zip',
        'Prism-0.1.0-rc.4-x64.zip',
      ])
      expect(mac.files.map(entry => entry.sha512).sort()).toEqual(['arm', 'x64'])
    } finally {
      await rm(root, { recursive: true, force: true })
    }
  })
})

describe('PRISM_DAEMON_TARGETS', () => {
  it('rejects an unknown or repeated daemon name before compiling', async () => {
    const { selectDaemonTargets } = await import('../daemon-targets.mjs')
    const targets = [
      { name: 'prismd-darwin-arm64' },
      { name: 'prismd-linux-amd64' },
    ]
    expect(selectDaemonTargets(targets, undefined).map(target => target.name)).toEqual(['prismd-darwin-arm64', 'prismd-linux-amd64'])
    expect(selectDaemonTargets(targets, 'prismd-darwin-arm64').map(target => target.name)).toEqual(['prismd-darwin-arm64'])
    expect(() => selectDaemonTargets(targets, 'prismd-nope')).toThrow(/unknown PRISM_DAEMON_TARGETS: prismd-nope/)
    expect(() => selectDaemonTargets(targets, 'prismd-darwin-arm64,prismd-darwin-arm64')).toThrow(/unknown PRISM_DAEMON_TARGETS/)
  })
})
