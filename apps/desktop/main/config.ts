import { DAEMON_DEFAULT_PORT } from '@prism/contracts'

export const PRISMD_PATH_ENV = 'PRISMD_PATH'
export const PRISM_PORT_ENV = 'PRISM_PORT'
export const PRISM_DAEMON_CONFIG_ENV = 'PRISM_DAEMON_CONFIG'
export const PRISM_RENDERER_URL_ENV = 'PRISM_RENDERER_URL'
export const PRISM_HEADLESS_ENV = 'PRISM_HEADLESS'

export interface DesktopConfig {
  readonly port: number
  readonly daemonConfigPath: string | null
  readonly headless: boolean
}

export function loadDesktopConfig(env: NodeJS.ProcessEnv = process.env): DesktopConfig {
  return {
    port: parsePort(env[PRISM_PORT_ENV]),
    daemonConfigPath: parseOptionalPath(env[PRISM_DAEMON_CONFIG_ENV]),
    headless: parseHeadless(env[PRISM_HEADLESS_ENV]),
  }
}

function parsePort(raw: string | undefined): number {
  if (raw === undefined || raw === '') return DAEMON_DEFAULT_PORT
  const port = Number(raw)
  if (!Number.isInteger(port) || port < 1 || port > 65535) {
    throw new Error(`prism: ${PRISM_PORT_ENV} must be an integer between 1 and 65535, got ${raw}`)
  }
  return port
}

function parseOptionalPath(raw: string | undefined): string | null {
  if (raw === undefined || raw === '') return null
  return raw
}

function parseHeadless(raw: string | undefined): boolean {
  if (raw === undefined || raw === '') return false
  return raw === '1' || raw.toLowerCase() === 'true'
}
