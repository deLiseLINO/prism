import { useState } from 'react'

import type { UpdaterStatus } from '@prism/contracts'
import { bridge } from '../bridge'

type DotTone = 'ok' | 'info' | 'warn' | 'danger'

interface Row {
  readonly dot: DotTone
  readonly pulse: boolean
  readonly label: string
  readonly retryable: boolean
}

// Silent states (idle, up-to-date, disabled) keep the footer to the daemon row alone.
function rowFor(status: UpdaterStatus): Row | null {
  switch (status.state) {
    case 'downloaded':
      return { dot: 'ok', pulse: false, label: `update ${status.downloadedVersion ?? ''} ready`.trim(), retryable: false }
    case 'downloading':
      return { dot: 'info', pulse: false, label: 'downloading', retryable: false }
    case 'checking':
      return { dot: 'info', pulse: true, label: 'checking', retryable: false }
    case 'available':
      return { dot: 'info', pulse: false, label: `update ${status.availableVersion ?? ''}`.trim(), retryable: false }
    case 'installing':
      return { dot: 'warn', pulse: false, label: 'installing', retryable: false }
    case 'error':
      return { dot: 'danger', pulse: false, label: 'update error', retryable: status.canRetry }
    default:
      return null
  }
}

export function UpdateMini({ status }: { readonly status: UpdaterStatus | null }): JSX.Element | null {
  const [installing, setInstalling] = useState(false)
  const [installError, setInstallError] = useState<string | null>(null)

  if (status === null) return null
  const row = rowFor(status)
  if (row === null) return null

  const dotClass = `dot dot-${row.dot}${row.pulse ? ' dot-pulse' : ''}`

  const install = (): void => {
    if (installing) return
    setInstalling(true)
    bridge.updater.install().then(
      () => setInstalling(false),
      (error: unknown) => {
        setInstallError(error instanceof Error ? error.message : String(error))
        setInstalling(false)
      },
    )
  }

  if (status.state === 'downloaded') {
    return (
      <div className="update-mini" title={installError ?? undefined}>
        <span className={dotClass} aria-hidden="true" />
        <span>{row.label}</span>
        <button type="button" className="btn btn--primary btn--sm" disabled={installing} onClick={install}>
          Restart
        </button>
      </div>
    )
  }

  if (row.retryable) {
    return (
      <button type="button" className="update-mini update-mini--retry" title={status.error ?? undefined} onClick={() => void bridge.updater.check()}>
        <span className={dotClass} aria-hidden="true" />
        <span>{row.label}</span>
      </button>
    )
  }

  return (
    <div className="update-mini" title={status.error ?? undefined}>
      <span className={dotClass} aria-hidden="true" />
      <span>{row.label}</span>
      {status.state === 'downloading' && status.progress !== null ? (
        <span className="num update-mini__pct">{Math.round(status.progress)}%</span>
      ) : null}
    </div>
  )
}
