import { useEffect, useRef, useState } from 'react'
import type { AuthSessionState, AuthStartView } from '@prism/contracts'
import { Banner, Button, ErrorToast } from './Ui'
import { Dialog, DialogSection } from './Patterns'
import { useTask, describeError } from '../useAsync'
import { api, ApiError } from '../api'
import { bridge } from '../bridge'

const POLL_INTERVAL_MS = 1_500

const TERMINAL: Record<AuthSessionState, boolean> = {
  pending: false,
  complete: true,
  failed: true,
  authorized: true,
  unauthorized: true,
  unknown: false,
}

function sessionTone(state: AuthSessionState): 'ok' | 'error' | 'info' {
  if (state === 'complete' || state === 'authorized') return 'ok'
  if (state === 'failed' || state === 'unauthorized') return 'error'
  return 'info'
}

interface AuthSession {
  readonly session: string
  readonly url: string
  readonly userCode: string
  readonly state: AuthSessionState
}

const LOGIN_PROVIDERS = [
  { id: 'codex', label: 'codex' },
  { id: 'antigravity', label: 'antigravity' },
  { id: 'cline', label: 'cline' },
] as const

export interface AddAccountModalProps {
  readonly onAdded: () => void
  readonly onClose: () => void
}

export function AddAccountModal({
  onAdded,
  onClose,
}: AddAccountModalProps): JSX.Element {
  const [providerId, setProviderId] = useState<string>(LOGIN_PROVIDERS[0].id)
  const [session, setSession] = useState<AuthSession | null>(null)
  const [pollFailure, setPollFailure] = useState<'expired' | 'interrupted' | null>(null)
  const [copied, setCopied] = useState(false)
  const sessionRef = useRef<string | null>(null)
  const notifiedRef = useRef(false)
  const task = useTask()
  const [toast, setToast] = useState<string | null>(null)
  const [starting, setStarting] = useState(false)

  const selected = LOGIN_PROVIDERS.find((provider) => provider.id === providerId) ?? LOGIN_PROVIDERS[0]

  useEffect(() => {
    if (session === null || TERMINAL[session.state]) return
    const id = window.setTimeout(() => {
      void checkStatus()
    }, POLL_INTERVAL_MS)
    return () => {
      window.clearTimeout(id)
    }
  }, [session])

  async function checkStatus(): Promise<void> {
    if (session === null || selected === undefined) return
    const id = session.session
    try {
      const status = await api.authStatus(selected.id, id)
      if (sessionRef.current !== id) return
      setSession((prev) =>
        prev !== null && prev.session === id ? { ...prev, state: status.state } : prev,
      )
      setPollFailure(null)
      if (
        TERMINAL[status.state] &&
        (status.state === 'complete' || status.state === 'authorized') &&
        !notifiedRef.current
      ) {
        notifiedRef.current = true
        onAdded()
      }
    } catch (error: unknown) {
      if (sessionRef.current !== id) return
      if (
        error instanceof ApiError &&
        (error.code === 'session_expired' || error.code === 'unknown_session')
      ) {
        setPollFailure('expired')
        return
      }
      setPollFailure('interrupted')
    }
  }

  async function start(): Promise<void> {
    sessionRef.current = null
    notifiedRef.current = false
    setSession(null)
    setPollFailure(null)
    setToast(null)
    setStarting(true)
    try {
      const started = await api.authStart(selected.id)
      sessionRef.current = started.session
      setSession({
        session: started.session,
        url: started.url,
        userCode: started.userCode ?? '',
        state: 'pending',
      })
    } catch (error: unknown) {
      const normalized = error instanceof Error ? error : new Error(String(error))
      setToast(describeError(normalized))
    } finally {
      setStarting(false)
    }
  }

  function cancelLogin(): void {
    sessionRef.current = null
    setSession(null)
    setPollFailure(null)
  }

  async function copyLink(): Promise<void> {
    if (session === null) return
    await bridge.shell.writeClipboard(session.url)
    setCopied(true)
    window.setTimeout(() => {
      setCopied(false)
    }, 1_500)
  }

  const approved =
    session !== null && (session.state === 'complete' || session.state === 'authorized')
  const failed =
    pollFailure !== null ||
    (session !== null && (session.state === 'failed' || session.state === 'unauthorized'))

  return (
    <Dialog
      label="Add account"
      title="Add account"
      subtitle={
        <>
          <span className="badge badge--muted num">{selected.id}</span>
          {session !== null ? (
            <span className={`badge badge--${sessionTone(session.state)} num`}>
              {session.state}
            </span>
          ) : null}
        </>
      }
      onClose={onClose}
      footer={
        <>
          <span className="ctl-spacer" />
          {session === null ? (
            <>
              <Button tone="ghost" onClick={onClose} disabled={starting}>
                Close
              </Button>
              <Button
                tone="primary"
                onClick={() => {
                  void start()
                }}
                disabled={starting}
                busy={starting}
              >
                Start login
              </Button>
            </>
          ) : TERMINAL[session.state] ? (
            <>
              <Button tone="ghost" onClick={onClose}>
                Close
              </Button>
              <Button
                tone="primary"
                onClick={() => {
                  void start()
                }}
                disabled={task.running}
                busy={task.running}
              >
                Start another login
              </Button>
            </>
          ) : (
            <>
              <Button tone="danger" onClick={cancelLogin}>
                Cancel login
              </Button>
              <Button
                tone="ghost"
                onClick={() => {
                  void copyLink()
                }}
              >
                {copied ? 'Copied' : 'Copy link'}
              </Button>
              <Button
                tone="primary"
                title="Open the authorization page in your default browser."
                onClick={() => {
                  void bridge.shell.openExternal(session.url)
                }}
              >
                Open in browser
              </Button>
            </>
          )}
        </>
      }
      error={
        toast !== null ? (
          <ErrorToast title="Login failed to start" detail={toast} onClose={() => setToast(null)} />
        ) : undefined
      }
    >
      <DialogSection label="Provider">
        <div className="seg" role="group" aria-label="Login provider">
          {LOGIN_PROVIDERS.map((provider) => (
            <button
              key={provider.id}
              type="button"
              className="seg-btn"
              aria-pressed={selected.id === provider.id}
              onClick={() => setProviderId(provider.id)}
              disabled={session !== null}
            >
              {provider.label}
            </button>
          ))}
        </div>
        <select
          id="add-account-provider"
          className="sr-only"
          value={selected.id}
          onChange={(e) => setProviderId(e.target.value)}
          tabIndex={-1}
          aria-hidden="true"
          disabled={session !== null}
        >
          {LOGIN_PROVIDERS.map((provider) => (
            <option key={provider.id} value={provider.id}>{provider.label}</option>
          ))}
        </select>
      </DialogSection>
      {session !== null && !TERMINAL[session.state] ? (
        <DialogSection label="Login">
          {session.userCode !== '' ? (
            <p className="note">
              Enter code <span className="num">{session.userCode}</span> in the browser, then wait here.
            </p>
          ) : (
            <p className="note">
              Complete the login in your browser. This window checks
              automatically until it finishes.
            </p>
          )}
        </DialogSection>
      ) : null}
      {approved ? (
        <DialogSection>
          <Banner tone="ok" title="Authorized">
            The account joined the pool.
          </Banner>
        </DialogSection>
      ) : null}
      {failed && !approved ? (
        <DialogSection>
          <Banner tone="error" title="Authorization failed">
            Start a new login to retry.
          </Banner>
        </DialogSection>
      ) : null}
    </Dialog>
  )
}
