import { useEffect, useRef, useState } from 'react'
import type { AccountsView, AuthSessionState, AuthStartView, ProvidersView } from '@prism/contracts'
import { AsyncBoundary, Banner, Button, Card, Row, Stack, Toggle } from '../components/Ui'
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

function AuthCard({ entry }: AuthCardProps): JSX.Element {
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

  return (
    <Card title={`${entry.pretty} login`} description={`OAuth handoff for ${entry.provider} provider.`} tone={tone}>
      <div className="auth-steps" aria-hidden="true">
        <span className={`auth-steps__step ${session !== null ? 'auth-steps__step--done' : 'auth-steps__step--now'}`}>
          <span className="auth-steps__n">1</span> Start
        </span>
        <span className="auth-steps__line" />
        <span className={`auth-steps__step ${session !== null && session.state !== 'pending' ? 'auth-steps__step--done' : session !== null ? 'auth-steps__step--now' : ''}`}>
          <span className="auth-steps__n">2</span> Approve
        </span>
        <span className="auth-steps__line" />
        <span className={`auth-steps__step ${session !== null && TERMINAL[session.state] ? 'auth-steps__step--done' : ''}`}>
          <span className="auth-steps__n">3</span> Done
        </span>
      </div>
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
      <Row gap="loose" align="start">
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
      </Row>
      {task.error !== null ? (
        <Banner tone="error" title="Could not start the login">
          {describeError(task.error)}
          {task.error instanceof ApiError && task.error.code === 'loopback_unavailable' ? (
            <>: the loopback callback port is busy. Free it and retry.</>
          ) : null}
        </Banner>
      ) : null}
      {session !== null ? (
        <div className="session-box">
          <Row gap="tight" align="center">
            <Toggle
              checked={poll}
              onChange={setPoll}
              label={`Poll ${entry.pretty}`}
              disabled={TERMINAL[session.state]}
            />
            {!TERMINAL[session.state] && poll ? <span className="badge badge--info">Polling</span> : null}
          </Row>
          <p className="meta">
            Session started {session.startedAt}. Codes, tokens, and state values are not shown here.
          </p>
        </div>
      ) : null}
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
                <ul className="bare-list">
                  {filtered.map((account) => (
                    <li key={account.id}>
                      <span className="bare-list__primary">{account.id}</span>
                      <span className="badge">{account.state}</span>
                    </li>
                  ))}
                </ul>
              )
            }}
          </AsyncBoundary>
        )}
      </AsyncBoundary>
    </Card>
  )
}

export function AuthView(): JSX.Element {
  return (
    <Stack gap="normal">
      <Banner tone="info" title="Auth handoff">
        Prism opens the provider URL in your default browser. Approval lands here via the daemon’s loopback callback.
        Tokens and codes never appear in this UI.
      </Banner>
      <div className="auth-grid">
        {PROVIDERS.map((entry) => (
          <AuthCard key={entry.provider} entry={entry} />
        ))}
      </div>
    </Stack>
  )
}
