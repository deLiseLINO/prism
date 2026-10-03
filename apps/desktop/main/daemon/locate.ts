import { app } from 'electron'
import path from 'node:path'
import { existsSync } from 'node:fs'
import { chmod, copyFile, mkdir, rename, rm } from 'node:fs/promises'
import { PRISMD_PATH_ENV } from '../config'
import { runCli } from './cli'

const VERSION_TIMEOUT_MS = 10_000
const SAFE_VERSION = /^[0-9A-Za-z][0-9A-Za-z._+-]*$/

export interface DaemonBinary {
  readonly path: string
  readonly source: 'configured' | 'bundled'
}

export function locateDaemon(env: NodeJS.ProcessEnv = process.env): DaemonBinary {
  const configured = env[PRISMD_PATH_ENV]
  if (configured !== undefined && configured !== '') {
    if (!existsSync(configured)) {
      throw new Error(`prism: ${PRISMD_PATH_ENV} points to a missing file: ${configured}`)
    }
    return { path: configured, source: 'configured' }
  }
  const bundled = bundledDaemonPath()
  if (!existsSync(bundled)) {
    throw new Error(`prism: bundled prism binary not found at ${bundled}; run npm run build --workspace @prism/desktop or set ${PRISMD_PATH_ENV}`)
  }
  return { path: bundled, source: 'bundled' }
}

export async function stageDaemon(binary: DaemonBinary): Promise<string> {
  if (binary.source === 'configured') return binary.path
  const version = await bundledVersion(binary.path)
  const target = path.join(app.getPath('userData'), 'cli', version, process.platform === 'win32' ? 'prism.exe' : 'prism')
  if (existsSync(target)) return target
  await mkdir(path.dirname(target), { recursive: true })
  const temp = `${target}.${process.pid}.tmp`
  try {
    await copyFile(binary.path, temp)
    await chmod(temp, 0o755)
    await rename(temp, target)
  } catch (error) {
    await rm(temp, { force: true })
    throw error
  }
  return target
}

async function bundledVersion(binaryPath: string): Promise<string> {
  const result = await runCli(binaryPath, ['version'], VERSION_TIMEOUT_MS)
  const version = result.stdout.trim()
  if (result.code !== 0 || !SAFE_VERSION.test(version)) {
    throw new Error(`prism: cannot read the version of ${binaryPath}: ${result.stderr.trim() || JSON.stringify(version)}`)
  }
  return version
}

// build.mjs compiles one binary per supported platform/arch so a packaged app
// works regardless of the machine that produced the installer.
export function bundledDaemonBinary(platform: NodeJS.Platform = process.platform, arch: string = process.arch): string {
  const goarch = arch === 'x64' ? 'amd64' : arch === 'arm64' ? 'arm64' : ''
  if (!goarch) {
    throw new Error(`prism: unsupported architecture ${arch}`)
  }
  const os = platform === 'darwin' ? 'darwin' : platform === 'linux' ? 'linux' : platform === 'win32' ? 'windows' : null
  if (!os) {
    throw new Error(`prism: unsupported platform ${platform}`)
  }
  const suffix = platform === 'win32' ? '.exe' : ''
  return `prism-${os}-${goarch}${suffix}`
}

function bundledDaemonPath(platform: NodeJS.Platform = process.platform, arch: string = process.arch): string {
  return app.isPackaged
    ? path.join(process.resourcesPath, 'prism', bundledDaemonBinary(platform, arch))
    : path.join(app.getAppPath(), 'resources', 'prism', bundledDaemonBinary(platform, arch))
}

export function locateWebui(configured: string | null): string | null {
  if (configured !== null && configured !== '') {
    return existsSync(configured) ? configured : null
  }
  const bundled = app.isPackaged
    ? path.join(process.resourcesPath, 'webui')
    : path.join(app.getAppPath(), 'resources', 'webui')
  return existsSync(bundled) ? bundled : null
}
