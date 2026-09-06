export const DAEMON_DEFAULT_PORT = 10200
export const DAEMON_HOST = '127.0.0.1'
export const DAEMON_HEALTH_PATH = '/api/v1/health'

export type DaemonState =
  | 'idle'
  | 'starting'
  | 'ready'
  | 'backoff'
  | 'stopping'
  | 'stopped'
  | 'failed'
  | 'quitting'

export interface DaemonExit {
  readonly code: number | null
  readonly signal: string | null
}

export interface DaemonStatus {
  readonly state: DaemonState
  readonly attempt: number
  readonly pid: number | null
  readonly endpoint: string | null
  readonly startedAt: string | null
  readonly lastExit: DaemonExit | null
  readonly lastError: string | null
}
