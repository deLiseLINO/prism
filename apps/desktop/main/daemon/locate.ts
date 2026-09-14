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

function bundledDaemonPath(): string {
  const suffix = process.platform === 'win32' ? '.exe' : ''
  const dir = app.isPackaged
    ? path.join(process.resourcesPath, 'prismd')
    : path.join(app.getAppPath(), 'resources', 'prismd')
  const plain = path.join(dir, `prismd${suffix}`)
  if (!app.isPackaged || process.platform !== 'linux') {
    return plain
  }
  // Cross-built linux bundles ship arch-suffixed binaries (prismd-linux-amd64 / prismd-linux-arm64);
  // the plain `prismd` may be a foreign-OS binary, so prefer the suffixed one for the current arch.
  const archSuffix = process.arch === 'arm64' ? 'linux-arm64' : 'linux-amd64'
  const suffixed = path.join(dir, `prismd-${archSuffix}`)
  return existsSync(suffixed) ? suffixed : plain
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
