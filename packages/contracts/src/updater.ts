export type UpdaterState =
  | 'disabled'
  | 'idle'
  | 'checking'
  | 'available'
  | 'downloading'
  | 'downloaded'
  | 'up-to-date'
  | 'installing'
  | 'error'

export type UpdaterErrorStage = 'check' | 'download' | 'install'

export interface UpdaterStatus {
  readonly state: UpdaterState
  readonly currentVersion: string
  readonly availableVersion: string | null
  readonly downloadedVersion: string | null
  readonly progress: number | null
  readonly error: string | null
  readonly errorStage: UpdaterErrorStage | null
  readonly canRetry: boolean
  readonly lastCheckedAt: string | null
}
