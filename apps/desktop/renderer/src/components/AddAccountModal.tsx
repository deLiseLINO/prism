import { useEffect, useRef, useState, type KeyboardEvent } from 'react'
import { createPortal } from 'react-dom'
import type { AuthSessionState, AuthStartView, ProviderView } from '@prism/contracts'
import { Banner, Button } from './Ui'
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
  readonly state: AuthSessionState
}

export interface AddAccountModalProps {
  readonly providers: readonly ProviderView[]
  readonly onAdded: () => void
  readonly onClose: () => void
}

export function AddAccountModal({
  providers,
  onAdded,
  onClose,
}: AddAccountModalProps): JSX.Element {
  const [providerId, setProviderId] = useState(providers[0]?.id ?? '')
  const [session, setSession] = useState<AuthSession | null>(null)
  const [pollFailure, setPollFailure] = useState<'expired' | 'interrupted' | null>(null)
  const [copied, setCopied] = useState(false)
  const sessionRef = useRef<string | null>(null)
  const notifiedRef = useRef(false)
  const task = useTask()

  const selected =
    providers.find((provider) => provider.id === providerId) ?? providers[0]

  useEffect(() => {
    const onKey = (event: globalThis.KeyboardEvent): void => {
      if (event.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

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
    if (selected === undefined) return
    sessionRef.current = null
    notifiedRef.current = false
    setSession(null)
    setPollFailure(null)
    const started = await task.run<AuthStartView>(() => api.authStart(selected.id))
    if (started === undefined) return
    sessionRef.current = started.session
    setSession({ session: started.session, url: started.url, state: 'pending' })
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

  function onDialogKey(event: KeyboardEvent): void {
    if (event.key === 'Escape') {
      event.stopPropagation()
      onClose()
    }
  }

  const approved =
    session !== null && (session.state === 'complete' || session.state === 'authorized')
  const failed =
    pollFailure !== null ||
    (session !== null && (session.state === 'failed' || session.state === 'unauthorized'))

  return createPortal(
    <div className="msm-veil" role="presentation" onClick={onClose}>
      <section
        className="msm card"
        role="dialog"
        aria-modal="true"
        aria-label="Add account"
        onClick={(event) => event.stopPropagation()}
        onKeyDown={onDialogKey}
      >
        <header className="msm-head">
          <div>
            <div className="msm-title num">Add account</div>
            <div className="msm-sub">
              <span className="badge badge--muted num">{selected?.wire ?? 'login'}</span>
              {session !== null ? (
                <span className={`badge badge--${sessionTone(session.state)} num`}>
                  {session.state}
                </span>
              ) : null}
              <span className="msm-hint">OAuth login, the account joins the pool on completion</span>
            </div>
          </div>
          <button type="button" className="ibtn" aria-label="Close" onClick={onClose}>
            <svg width="13" height="13" viewBox="0 0 14 14" fill="none" stroke="currentColor" strokeWidth="1.2" strokeLinecap="round"><path d="M2.5 2.5l9 9M11.5 2.5l-9 9" /></svg>
          </button>
        </header>
        <div className="msm-body">
          {providers.length === 0 ? (
            <section className="msm-sec">
              <p className="note">
                No providers with a login flow. Add a codex or antigravity provider
                first.
              </p>
            </section>
          ) : (
            <>
              <section className="msm-sec">
                <div className="msm-sec-label">Provider</div>
                <div className="pmod-wire">
                  <span className="pmod-wire-label">Provider</span>
                  <div className="msm-seg" role="group" aria-label="Login provider">
                    {providers.map((provider) => (
                      <button
                        key={provider.id}
                        type="button"
                        className="msm-seg-btn"
                        aria-pressed={(selected?.id ?? '') === provider.id}
                        onClick={() => setProviderId(provider.id)}
                        disabled={session !== null}
                      >
                        {provider.id}
                      </button>
                    ))}
                  </div>
                  <select
                    id="add-account-provider"
                    className="sr-only"
                    value={selected?.id ?? ''}
                    onChange={(e) => setProviderId(e.target.value)}
                    tabIndex={-1}
                    aria-hidden="true"
                    disabled={session !== null}
                  >
                    {providers.map((provider) => (
                      <option key={provider.id} value={provider.id}>{provider.id}</option>
                    ))}
                  </select>
                </div>
              </section>
              {session !== null && !TERMINAL[session.state] ? (
                <section className="msm-sec">
                  <div className="msm-sec-label">Login</div>
                  <p className="note">
                    Complete the login in your browser. This window checks
                    automatically until it finishes.
                  </p>
                </section>
              ) : null}
              {approved ? (
                <section className="msm-sec">
                  <Banner tone="ok" title="Authorized">
                    The account joined the pool.
                  </Banner>
                </section>
              ) : null}
              {failed && !approved ? (
                <section className="msm-sec">
                  <Banner tone="error" title="Authorization failed">
                    Start a new login to retry.
                  </Banner>
                </section>
              ) : null}
            </>
          )}
        </div>
        {providers.length > 0 ? (
          <footer className="msm-foot">
            <span className="msm-spacer" />
            {session === null ? (
              <>
                <Button tone="ghost" size="sm" onClick={onClose} disabled={task.running}>
                  Close
                </Button>
                <Button
                  tone="primary"
                  size="sm"
                  onClick={() => {
                    void start()
                  }}
                  disabled={task.running}
                  busy={task.running}
                >
                  Start login
                </Button>
              </>
            ) : TERMINAL[session.state] ? (
              <>
                <Button tone="ghost" size="sm" onClick={onClose}>
                  Close
                </Button>
                <Button
                  tone="primary"
                  size="sm"
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
                <Button tone="danger" size="sm" onClick={cancelLogin}>
                  Cancel login
                </Button>
                <Button
                  tone="ghost"
                  size="sm"
                  onClick={() => {
                    void copyLink()
                  }}
                >
                  {copied ? 'Copied' : 'Copy link'}
                </Button>
                <Button
                  tone="primary"
                  size="sm"
                  title="Open the authorization page in your default browser."
                  onClick={() => {
                    void bridge.shell.openExternal(session.url)
                  }}
                >
                  Open in browser
                </Button>
              </>
            )}
          </footer>
        ) : null}
        {task.error !== null ? (
          <div className="pmod-error">
            <Banner tone="error" title="Login failed to start">
              {describeError(task.error)}
              {task.error instanceof ApiError && task.error.code === 'loopback_unavailable'
                ? ': the loopback callback port is busy. Free it and retry.'
                : null}
            </Banner>
          </div>
        ) : null}
      </section>
    </div>,
    document.body,
  )
}
