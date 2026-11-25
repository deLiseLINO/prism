export type HostInstallPhase = 'probe' | 'upload' | 'start' | 'verify' | 'register'

// How prism is kept running on a remote machine: a systemd user service, a
// launchd agent, or a plain background process that does not survive a reboot.
export type HostInstallService = 'systemd' | 'launchd' | 'none'

// Outcome of one install, as POST /api/v1/hosts/{host}/install reports it.
export type HostInstallReply =
  | {
    readonly ok: true
    readonly host: string
    readonly version: string
    // True when a prism the daemon did not start already runs there and was kept.
    readonly adopted?: boolean
    // Absent for an adopted prism, which the daemon does not manage.
    readonly service?: HostInstallService
    // Something the user should act on, such as no start after reboot.
    readonly notice?: string
  }
  | { readonly ok: false; readonly host: string; readonly phase: HostInstallPhase; readonly reason: string }

// GET /api/v1/hosts/{host}/install: the latest install of one machine. reply
// is null until an install finishes.
export interface HostInstallStatus {
  readonly running: boolean
  readonly reply: HostInstallReply | null
}
