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
  const binary = process.platform === 'win32' ? 'prismd.exe' : 'prismd'
  return app.isPackaged
    ? path.join(process.resourcesPath, 'prismd', binary)
    : path.join(app.getAppPath(), 'resources', 'prismd', binary)
}
