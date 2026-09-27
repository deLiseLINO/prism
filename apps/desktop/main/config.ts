import net from 'node:net'
import { DAEMON_DEFAULT_PORT, DAEMON_HOST } from '@prism/contracts'

export const PRISMD_PATH_ENV = 'PRISMD_PATH'
export const PRISM_PORT_ENV = 'PRISM_PORT'
export const PRISM_DAEMON_CONFIG_ENV = 'PRISM_DAEMON_CONFIG'
export const PRISM_RENDERER_URL_ENV = 'PRISM_RENDERER_URL'
export const PRISM_HEADLESS_ENV = 'PRISM_HEADLESS'
export const PRISM_USER_DATA_ENV = 'PRISM_USER_DATA'
export const PRISM_WEBUI_DIR_ENV = 'PRISM_WEBUI_DIR'

export interface DesktopConfig {
  readonly port: number
  readonly headless: boolean
  readonly userDataPath: string | null
  readonly daemonConfigPath: string | null
  readonly webuiDir: string | null
}

const AUTO_PORT_MIN = 10201
const AUTO_PORT_MAX = 10250

export async function loadDesktopConfig(env: NodeJS.ProcessEnv = process.env): Promise<DesktopConfig> {
  return {
    port: await resolvePort(env[PRISM_PORT_ENV]),
    daemonConfigPath: parseOptionalPath(env[PRISM_DAEMON_CONFIG_ENV]),
    headless: parseHeadless(env[PRISM_HEADLESS_ENV]),
    userDataPath: parseOptionalPath(env[PRISM_USER_DATA_ENV]),
    webuiDir: parseOptionalPath(env[PRISM_WEBUI_DIR_ENV]),
  }
}


async function resolvePort(raw: string | undefined): Promise<number> {
  if (raw === undefined || raw === '') return DAEMON_DEFAULT_PORT
  if (raw !== 'auto') return parsePort(raw)
  const port = await firstFreePort()
  if (port === null) {
    throw new Error(`prism: ${PRISM_PORT_ENV}=auto found no free ports in ${AUTO_PORT_MIN}-${AUTO_PORT_MAX}`)
  }
  console.info(`prism: selected daemon port ${port}`)
  return port
}

async function firstFreePort(): Promise<number | null> {
  for (let port = AUTO_PORT_MIN; port <= AUTO_PORT_MAX; port++) {
    if (await isFreePort(port)) return port
  }
  return null
}

function isFreePort(port: number): Promise<boolean> {
  return new Promise((resolve) => {
    const server = net.createServer()
    server.once('error', () => resolve(false))
    server.listen(port, DAEMON_HOST, () => {
      server.close(() => resolve(true))
    })
  })
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
