export type HostInstallPhase = 'probe' | 'upload' | 'start' | 'verify' | 'register'

// How prism is kept running on a remote machine: a systemd user service, a
// launchd agent, or a plain background process that does not survive a reboot.
export type HostInstallService = 'systemd' | 'launchd' | 'none'

export interface HostInstallRequest {
  readonly host: string
}

export type HostInstallReply =
  | {
    readonly ok: true
    readonly host: string
    readonly version: string
    // True when a prism this app did not start already runs there and was kept.
    readonly adopted: boolean
    // null for an adopted prism, which this app does not manage.
    readonly service: HostInstallService | null
    // Something the user should act on, such as no start after reboot.
    readonly notice: string | null
  }
  | { readonly ok: false; readonly host: string; readonly phase: HostInstallPhase; readonly reason: string }
