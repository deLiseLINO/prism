export type HostInstallPhase = 'probe' | 'upload' | 'start' | 'verify' | 'register'

export interface HostInstallRequest {
  readonly host: string
}

export type HostInstallReply =
  | { readonly ok: true; readonly host: string; readonly version: string; readonly adopted: boolean }
  | { readonly ok: false; readonly host: string; readonly phase: HostInstallPhase; readonly reason: string }
