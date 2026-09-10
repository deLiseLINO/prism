import { useEffect, useState } from 'react'
import type { AgentJob, AgentJobRequest, AgentStatus } from '@prism/contracts'
import { Button } from '../components/Ui'
import { useTask, describeError } from '../useAsync'
import { bridge } from '../bridge'

const LIVE_STATES: ReadonlySet<string> = new Set(['running', 'installing', 'verifying'])
const POLL_MS = 700

interface InstallCellProps {
  readonly status: AgentStatus
  readonly onChanged: () => void
}

export function InstallCell({ status, onChanged }: InstallCellProps): JSX.Element {
  const [job, setJob] = useState<AgentJob>(status.job)
  const [actionError, setActionError] = useState<string | null>(null)
  const installTask = useTask()
  const updateTask = useTask()
  const busy = installTask.running || updateTask.running || LIVE_STATES.has(job.state)
  const taskError = installTask.error ?? updateTask.error

  useEffect(() => {
    setJob(status.job)
  }, [status.job])

  useEffect(() => {
    if (!LIVE_STATES.has(job.state)) return
    const timer = window.setInterval(() => {
      void bridge.agents.job({ id: status.id }).then((next) => {
        setJob(next)
        if (!LIVE_STATES.has(next.state)) onChanged()
      }, () => {})
    }, POLL_MS)
    return () => window.clearInterval(timer)
  }, [job.state, status.id, onChanged])

  async function install(): Promise<void> {
    const reply = await installTask.run(() => bridge.agents.install({ id: status.id }))
    if (reply === undefined) return
    if (!reply.ok) {
      setActionError(reply.reason === '' ? 'install refused without a reason' : reply.reason)
      return
    }
    setActionError(null)
    setJob(reply.job)
  }

  async function update(): Promise<void> {
    const reply = await updateTask.run(() => bridge.agents.update({ id: status.id }))
    if (reply === undefined) return
    if (!reply.ok) {
      setActionError(reply.reason === '' ? 'update refused without a reason' : reply.reason)
      return
    }
    setActionError(null)
    setJob(reply.job)
  }

  const live = LIVE_STATES.has(job.state)
  const failed = job.state === 'failed' || job.state === 'interrupted'
  const justSucceeded = job.state === 'succeeded' && !status.installed
  const source = status.installed ? (status.source === '' ? 'unknown' : status.source) : null

  return (
    <div className="int-install">
      {live ? (
        <span className="int-install__state" role="status">
          <span className="dot dot-pulse" aria-hidden="true" />
          {job.state}
          {job.command !== undefined && job.command !== '' ? (
            <span className="int-install__cmd" title={job.command}>{job.command}</span>
          ) : null}
        </span>
      ) : justSucceeded ? (
        <span className="int-install__state int-install__state--installed" role="status">
          <span className="dot dot-ok" aria-hidden="true" />
          {job.op === 'update' ? 'updated' : 'installed'}
        </span>
      ) : source !== null ? (
        <span className="int-install__state int-install__state--installed">
          <span className="dot dot-ok" aria-hidden="true" />
          {source}
        </span>
      ) : (
        <span className="int-install__state int-install__state--absent">
          <span className="dot dot-muted" aria-hidden="true" />
          not installed
        </span>
      )}
      <span className="int-install__actions">
        <Button
          tone={status.installed ? 'ghost' : 'primary'}
          size="sm"
          onClick={() => void install()}
          disabled={busy}
        >
          {status.installed ? 'Reinstall' : 'Install'}
        </Button>
        <Button
          tone="ghost"
          size="sm"
          onClick={() => void update()}
          disabled={busy || !status.canUpdate}
          title={status.installed && !status.canUpdate ? (status.reason ?? 'update unavailable') : undefined}
        >
          Update
        </Button>
      </span>
      {taskError !== null ? (
        <span className="int-install__err" role="alert">{describeError(taskError)}</span>
      ) : null}
      {taskError === null && actionError !== null ? (
        <span className="int-install__err" role="alert">{actionError}</span>
      ) : null}
      {taskError === null && actionError === null && failed ? (
        <span className="int-install__err" role="alert">
          {job.error === '' || job.error === undefined ? 'job failed without detail' : job.error}
        </span>
      ) : null}
    </div>
  )
}
