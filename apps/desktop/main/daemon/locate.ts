import { app } from 'electron'
import path from 'node:path'
import { existsSync } from 'node:fs'
import { PRISMD_PATH_ENV } from '../config'

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
    throw new Error(`prism: bundled prismd not found at ${bundled}; build it with go build -o resources/prismd/prismd ../../cmd/prismd or set ${PRISMD_PATH_ENV}`)
  }
  return { path: bundled, source: 'bundled' }
}

// Maps the running Electron process to the bundled prismd binary name.
// build.mjs compiles one binary per supported platform/arch so a packaged app
// works regardless of the machine that produced the dmg.
export function bundledDaemonBinary(platform: NodeJS.Platform = process.platform, arch: string = process.arch): string {
  if (platform === 'win32') return 'prismd.exe' // future windows support
  const goarch = arch === 'x64' ? 'amd64' : arch === 'arm64' ? 'arm64' : ''
  if (!goarch) {
    throw new Error(`prism: unsupported architecture ${arch}`)
  }
  return `prismd-${platform === 'darwin' ? 'darwin' : String(platform)}-${goarch}`
}

function bundledDaemonPath(platform: NodeJS.Platform = process.platform, arch: string = process.arch): string {
  return app.isPackaged
    ? path.join(process.resourcesPath, 'prismd', bundledDaemonBinary(platform, arch))
    : path.join(app.getAppPath(), 'resources', 'prismd', bundledDaemonBinary(platform, arch))
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
