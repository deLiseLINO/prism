import { useEffect, useRef, useState, type ReactNode } from 'react'
import type { DaemonStatus } from '@prism/contracts'

export type BootPhase =
  | { readonly kind: 'booting'; readonly label: string }
  | { readonly kind: 'gave-up' }

export const BOOT_GIVE_UP_MS = 20_000

export function resolveBootPhase(
  status: DaemonStatus | null,
  unreachable: boolean,
  gaveUp: boolean,
): BootPhase {
  if (unreachable) return { kind: 'gave-up' }
  if (status === null) return { kind: 'booting', label: 'waking up…' }
  if (status.state === 'ready') return { kind: 'gave-up' }
  if (status.state === 'failed') return { kind: 'gave-up' }
  if (gaveUp) return { kind: 'gave-up' }
  switch (status.state) {
    case 'starting':
      return { kind: 'booting', label: 'starting daemon…' }
    case 'backoff':
      return { kind: 'booting', label: `restarting (attempt ${status.attempt + 1})…` }
    case 'stopping':
    case 'stopped':
    case 'quitting':
      return { kind: 'booting', label: 'daemon stopped' }
    default:
      return { kind: 'booting', label: 'waking up…' }
  }
}

/**
 * Boot gate for the local daemon. Views must not mount before the first
 * ready: they fire management calls through a proxy with no daemon behind
 * it yet, and surface spurious errors at startup. The gate opens once on
 * ready and stays open; a failure or a 20s timeout hands the UI back with
 * the daemon's own error surfaces instead of an endless spinner.
 */
export function useDaemonBoot(
  status: DaemonStatus | null,
  unreachable: boolean,
): BootPhase {
  const [gaveUp, setGaveUp] = useState(false)
  const openedRef = useRef(false)
  if (status !== null && status.state === 'ready') openedRef.current = true
  const open = openedRef.current

  useEffect(() => {
    if (open || unreachable) return
    const timer = window.setTimeout(() => setGaveUp(true), BOOT_GIVE_UP_MS)
    return () => window.clearTimeout(timer)
  }, [open, unreachable])

  if (open) return { kind: 'gave-up' }
  return resolveBootPhase(status, unreachable, gaveUp)
}

export function BootScreen({
  phase,
}: {
  readonly phase: BootPhase
}): ReactNode {
  if (phase.kind !== 'booting') return null
  return (
    <div className="boot" role="status" aria-busy="true" aria-label={`Prism is starting: ${phase.label}`}>
      <svg className="boot-mark" width="52" height="52" viewBox="0 0 20 20" aria-hidden="true">
        <path d="M10 2 18.5 17H1.5L10 2Z" fill="none" stroke="currentColor" strokeWidth="1.1" strokeLinejoin="round" />
        <path d="M10 2v15M6.3 12.4 10 17l3.7-4.6" fill="none" stroke="currentColor" strokeWidth="0.8" opacity="0.55" />
      </svg>
      <p className="boot-name">Prism</p>
      <p className="boot-status">
        <span className="dot dot-muted dot-boot-pulse" aria-hidden="true" />
        <span>{phase.label}</span>
      </p>
      <span className="boot-line skel" aria-hidden="true" />
    </div>
  )
}
