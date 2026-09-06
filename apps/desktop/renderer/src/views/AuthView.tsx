import { useEffect, useRef, useState, type CSSProperties } from 'react'
import type { AccountsView, AuthSessionState, AuthStartView, ProvidersView } from '@prism/contracts'
import { AsyncBoundary, Banner, Button, Toggle } from '../components/Ui'
import { useAsync, useTask, describeError } from '../useAsync'
import { api, ApiError } from '../api'

interface AuthProviderEntry {
  readonly provider: string
  readonly pretty: string
}

const PROVIDERS: readonly AuthProviderEntry[] = [
  { provider: 'codex', pretty: 'Codex' },
  { provider: 'antigravity', pretty: 'Antigravity' },
]

interface AuthSession {
  readonly session: string
  readonly url: string
  readonly startedAt: string
  readonly state: AuthSessionState
}

interface AuthCardProps {
  readonly entry: AuthProviderEntry
  readonly index: number
}

const POLL_INTERVAL_MS = 1_500
const TERMINAL: Record<AuthSessionState, boolean> = {
  pending: false,
  complete: true,
  failed: true,
  authorized: true,
  unauthorized: true,
  unknown: false,
}

function AuthCard({ entry, index }: AuthCardProps): JSX.Element {
  const [session, setSession] = useState<AuthSession | null>(null)
  const [poll, setPoll] = useState(true)
  const [pollFailure, setPollFailure] = useState<'expired' | 'interrupted' | null>(null)
  const sessionRef = useRef<string | null>(null)
  const accounts = useAsync<AccountsView>(() => api.accounts(), [entry.provider])
  const providers = useAsync<ProvidersView>(() => api.providers(), [entry.provider])
  const task = useTask()

  useEffect(() => {
    if (session === null || !poll) return
    if (TERMINAL[session.state]) return
    const id = window.setTimeout(() => {
      void refresh(false)
    }, POLL_INTERVAL_MS)
    return () => {
      window.clearTimeout(id)
    }
  }, [session, poll])

  async function refresh(manual: boolean): Promise<void> {
    if (session === null) return
    const id = session.session
    try {
      const status = await api.authStatus(entry.provider, id)
      if (sessionRef.current !== id) return
      setSession((prev) => (prev !== null && prev.session === id ? { ...prev, state: status.state } : prev))
      setPollFailure(null)
      if (TERMINAL[status.state]) {
        setPoll(false)
        await accounts.refresh()
        return
      }
      if (manual) setPoll(true)
    } catch (error: unknown) {
      if (sessionRef.current !== id) return
      setPoll(false)
      if (error instanceof ApiError && (error.code === 'session_expired' || error.code === 'unknown_session')) {
        setPollFailure('expired')
        return
      }
      setPollFailure('interrupted')
    }
  }

  async function start(): Promise<void> {
    sessionRef.current = null
    setSession(null)
    setPollFailure(null)
    setPoll(true)
    const started = await task.run<AuthStartView>(() => api.authStart(entry.provider))
    if (started === undefined) return
    sessionRef.current = started.session
    setSession({
      session: started.session,
      url: started.url,
      startedAt: new Date().toISOString(),
      state: 'pending',
    })
  }

  async function cancel(): Promise<void> {
    sessionRef.current = null
    setSession(null)
    setPoll(false)
    setPollFailure(null)
  }

  const finished = session !== null && (TERMINAL[session.state] || pollFailure === 'expired')

  const headline = (() => {
    if (session === null) return 'Not signed in'
    if (pollFailure === 'expired') return 'Authorization session expired'
    switch (session.state) {
      case 'pending':
        return `Awaiting ${entry.pretty} approval`
      case 'complete':
      case 'authorized':
        return 'Authorized'
      case 'failed':
        return 'Authorization failed'
      case 'unauthorized':
        return 'Authorization cancelled'
      default:
        return session.state
    }
  })()

  const tone = ((): 'ok' | 'warn' | 'error' => {
    if (session === null) return 'warn'
    if (pollFailure === 'expired') return 'error'
    if (session.state === 'complete' || session.state === 'authorized') return 'ok'
    if (session.state === 'failed') return 'error'
    return 'warn'
  })()

  const stateTone = ((): 'ok' | 'warn' | 'error' | 'info' | 'muted' => {
    if (session === null) return 'muted'
    if (pollFailure === 'expired') return 'error'
    if (session.state === 'complete' || session.state === 'authorized') return 'ok'
    if (session.state === 'failed' || session.state === 'unauthorized') return 'error'
    if (session.state === 'pending' || session.state === 'unknown') return 'info'
    return 'muted'
  })()

  const stateLabel = session === null ? 'idle' : session.state

  const approved = session !== null && (session.state === 'complete' || session.state === 'authorized')
  const failedFlow =
    pollFailure === 'expired' || (session !== null && (session.state === 'failed' || session.state === 'unauthorized'))

  const stepClass = (n: number): string => {
    if (session === null) return n === 1 ? 'active' : ''
    if (n === 1) return 'done'
    if (n === 2) return failedFlow ? 'failed' : approved ? 'done' : 'active'
    return approved ? 'done' : ''
  }

  const step = (n: number, label: string): JSX.Element => (
    <div className={`step ${stepClass(n)}`.trim()}>
      <span className="idx">{n}</span>
      <span className="step-label">{label}</span>
      {n < 3 ? <span className="step-line" aria-hidden="true" /> : null}
    </div>
  )

  return (
    <section className="panel card auth-p" style={{ '--i': index + 1 } as CSSProperties}>
      <div className="auth-head">
        <div>
          <h3 className="auth-name">{entry.pretty} login</h3>
          <p className="sub">OAuth handoff for {entry.provider} provider.</p>
        </div>
        <span className={`badge badge--${stateTone}`}>{stateLabel}</span>
      </div>
      <div className="steps" aria-hidden="true">
        {step(1, 'Start')}
        {step(2, 'Poll')}
        {step(3, 'Complete')}
      </div>
      <p className="flow-status">
        {session === null ? (
          'no active session, start a login to authorize a new account'
        ) : (
          <>
            session <span className="num">{session.session}</span>, status {stateLabel}
          </>
        )}
      </p>
      {session !== null ? (
        <div className="urls">
          <div className="url">
            <div className="k">Authorize</div>
            <div className="v">{session.url}</div>
          </div>
          <div className="url">
            <div className="k">Started</div>
            <div className="v">{session.startedAt}</div>
          </div>
        </div>
      ) : null}
      <Banner tone={tone} title={headline}>
        {session === null
          ? 'Start a new session to authorize a new account.'
          : pollFailure === 'expired'
          ? 'The authorization session expired or was already consumed. Start a new login.'
          : pollFailure === 'interrupted'
          ? 'Polling interrupted — Check status.'
          : session.state === 'pending'
          ? 'Complete the flow in your browser. Polling stops automatically on completion.'
          : null}
      </Banner>
      {task.error !== null ? (
        <div className="alert" role="alert">
          {describeError(task.error)}
          {task.error instanceof ApiError && task.error.code === 'loopback_unavailable' ? (
            <>: the loopback callback port is busy. Free it and retry.</>
          ) : null}
        </div>
      ) : null}
      <div className="auth-actions">
        {session === null ? (
          <Button
            tone="primary"
            onClick={() => {
              void start()
            }}
            disabled={task.running}
            busy={task.running}
          >
            Start {entry.pretty} login
          </Button>
        ) : finished ? (
          <>
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
            <Button tone="danger" onClick={() => void cancel()}>
              Cancel {entry.pretty} login
            </Button>
          </>
        ) : (
          <>
            <Button
              tone="primary"
              onClick={() => {
                void window.prism.shell.openExternal(session.url)
              }}
              title="Open authorization URL in your default browser."
            >
              Open {entry.pretty} authorization page
            </Button>
            <Button
              tone="ghost"
              onClick={() => {
                void refresh(true)
              }}
              disabled={poll}
            >
              Check {entry.pretty} status
            </Button>
            <Button tone="danger" onClick={() => void cancel()}>
              Cancel {entry.pretty} login
            </Button>
          </>
        )}
        <Toggle
          checked={poll}
          onChange={setPoll}
          label={`Poll ${entry.pretty}`}
          disabled={session === null || TERMINAL[session.state]}
        />
        {session !== null && !TERMINAL[session.state] && poll ? <span className="badge badge--info">Polling</span> : null}
        <span className="note">flows open a localhost callback and poll the device endpoint</span>
      </div>
      <AsyncBoundary<ProvidersView>
        state={providers.state}
        loadingLabel={`Loading ${entry.provider} providers…`}
        empty={<p className="meta">No {entry.provider} provider configured.</p>}
        onRetry={providers.refresh}
      >
        {(providerList) => (
          <AsyncBoundary<AccountsView>
            state={accounts.state}
            loadingLabel={`Loading ${entry.provider} accounts…`}
            empty={<p className="meta">No accounts for {entry.provider} yet.</p>}
            onRetry={accounts.refresh}
          >
            {(list) => {
              const ids = new Set(
                providerList.providers
                  .filter((provider) => provider.wire === entry.provider)
                  .map((provider) => provider.id),
              )
              const filtered = list.accounts.filter((account) => ids.has(account.provider))
              if (filtered.length === 0) {
                return <p className="meta">No accounts for {entry.provider} yet.</p>
              }
              return (
                <p className="note">
                  <span className="num">{filtered.length}</span> bound account{filtered.length === 1 ? '' : 's'}:{' '}
                  {filtered.map((account) => (
                    <span key={account.id}>
                      <span className="num">{account.id}</span> <span className="badge">{account.state}</span>{' '}
                    </span>
                  ))}
                </p>
              )
            }}
          </AsyncBoundary>
        )}
      </AsyncBoundary>
    </section>
  )
}

export function AuthView(): JSX.Element {
  return (
    <section className="screen" aria-labelledby="h-auth">
      <div className="screen-head" style={{ '--i': 0 } as CSSProperties}>
        <div>
          <h1 id="h-auth">
            <svg width="19" height="19" className="h-ic">
              <use href="#i-key" />
            </svg>
            Auth
          </h1>
          <p className="sub">OAuth device flows per provider, start to completion</p>
        </div>
      </div>
      <div className="divide" style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
        {PROVIDERS.map((entry, index) => (
          <AuthCard key={entry.provider} entry={entry} index={index} />
        ))}
      </div>
    </section>
  )
}
