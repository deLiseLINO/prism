import { useState } from 'react'
import type { CSSProperties } from 'react'
import type { IntegrationId, IntegrationStatus } from '@prism/contracts'
import { AsyncBoundary, Banner, Button, Empty } from '../components/Ui'
import { useAsync, useTask, describeError } from '../useAsync'

interface IntegrationRowProps {
  readonly status: IntegrationStatus
  readonly onChanged: () => void
}

function IntegrationRow({ status, onChanged }: IntegrationRowProps): JSX.Element {
  const applyTask = useTask()
  const rollbackTask = useTask()
  const busy = applyTask.running || rollbackTask.running
  const taskError = applyTask.error ?? rollbackTask.error

  const [actionError, setActionError] = useState<string | null>(null)

  async function apply(id: IntegrationId): Promise<void> {
    const result = await applyTask.run(() => window.prism.integrations.apply({ id }))
    if (result === undefined) return
    if (!result.ok) {
      setActionError(result.reason === '' ? 'reason not reported' : result.reason)
      return
    }
    setActionError(null)
    onChanged()
  }

  async function rollback(id: IntegrationId): Promise<void> {
    const result = await rollbackTask.run(() => window.prism.integrations.rollback({ id }))
    if (result === undefined) return
    if (!result.ok) {
      setActionError(result.reason === '' ? 'reason not reported' : result.reason)
      return
    }
    setActionError(null)
    onChanged()
  }

  return (
    <section className="panel card int">
      <div>
        <h3 className="int-name">{status.id}</h3>
        <div className="int-chips">
          <span className={`badge badge--${status.installed ? 'ok' : 'muted'}`}>
            {status.installed ? 'installed' : 'not installed'}
          </span>
          <span className={`badge badge--${status.managed ? 'ok' : 'muted'}`}>
            {status.managed ? 'managed' : 'unmanaged'}
          </span>
          {status.drift ? <span className="badge badge--warn">drift</span> : null}
        </div>
      </div>
      <div>
        {status.targetPath !== null && status.targetPath !== '' ? (
          <div className="int-path">{status.targetPath}</div>
        ) : (
          <div className="int-path" style={{ color: 'var(--fg-subtle)' }}>no config path</div>
        )}
        <div className="int-detail">{status.detail}</div>
      </div>
      <div className="int-actions">
        <Button
          tone={status.managed ? 'ghost' : 'primary'}
          size="sm"
          onClick={() => void apply(status.id)}
          disabled={!status.installed || busy}
          busy={applyTask.running}
        >
          Apply
        </Button>
        <Button
          tone="danger"
          size="sm"
          onClick={() => void rollback(status.id)}
          disabled={!status.managed || busy}
          busy={rollbackTask.running}
        >
          Rollback
        </Button>
        <span className="note">
          Rollback rewrites the {status.id} client configuration on disk
          {status.endpoint === null || status.endpoint === '' ? null : (
            <>
              {' '}
              via <span className="num">{status.endpoint}</span>
            </>
          )}
          .
        </span>
        {taskError !== null ? (
          <Banner tone="error" title="Integration action failed">
            {describeError(taskError)}
          </Banner>
        ) : null}
        {taskError === null && actionError !== null ? (
          <Banner tone="error" title="Integration action failed">
            {actionError}
          </Banner>
        ) : null}
      </div>
    </section>
  )
}

export function IntegrationsView(): JSX.Element {
  const statuses = useAsync<readonly IntegrationStatus[]>(() => window.prism.integrations.status(), [])
  return (
    <section className="screen" aria-labelledby="h-integrations">
      <div className="screen-head" style={{ '--i': 0 } as CSSProperties}>
        <div>
          <h1 id="h-integrations">
            <svg width="19" height="19" className="h-ic">
              <use href="#i-puzzle" />
            </svg>
            Integrations
          </h1>
          <p className="sub">config apply and rollback for each CLI Prism manages</p>
        </div>
      </div>
      <AsyncBoundary<readonly IntegrationStatus[]>
        state={statuses.state}
        loadingLabel="Loading integrations…"
        empty={<Empty title="No integrations reported." />}
        onRetry={() => statuses.refresh()}
      >
        {(list) => (
          <div className="stack" style={{ '--i': 1 } as CSSProperties}>
            {list.map((status) => (
              <IntegrationRow key={status.id} status={status} onChanged={statuses.refresh} />
            ))}
          </div>
        )}
      </AsyncBoundary>
    </section>
  )
}
