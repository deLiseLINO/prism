import { useState } from 'react'
import type { IntegrationId, IntegrationStatus } from '@prism/contracts'
import { AsyncBoundary, Banner, Button, Card, Empty, Row, Stack } from '../components/Ui'
import { useAsync, useTask, describeError } from '../useAsync'

interface IntegrationCardProps {
  readonly status: IntegrationStatus
  readonly onChanged: () => void
}

function IntegrationCard({ status, onChanged }: IntegrationCardProps): JSX.Element {
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
    <Card
      title={status.id}
      description={status.targetPath ?? '—'}
      tone={status.drift ? 'warn' : status.managed ? 'ok' : 'default'}
      action={
        <p className="meta">{status.endpoint ?? ''}</p>
      }
    >
      <Row gap="tight" align="start">
        <span className={`badge badge--${status.installed ? 'ok' : 'muted'}`}>
          {status.installed ? 'installed' : 'not installed'}
        </span>
        <span className={`badge badge--${status.managed ? 'ok' : 'muted'}`}>
          {status.managed ? 'managed' : 'unmanaged'}
        </span>
        {status.drift ? <span className="badge badge--warn">drift</span> : null}
      </Row>
      <p className="meta">{status.detail}</p>
      <Row gap="tight" align="start">
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
      </Row>
      <p className="meta">Rollback rewrites the {status.id} client configuration on disk.</p>
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
    </Card>
  )
}

export function IntegrationsView(): JSX.Element {
  const statuses = useAsync<readonly IntegrationStatus[]>(() => window.prism.integrations.status(), [])
  return (
    <Stack gap="normal">
      <Banner tone="info" title="Integrations">
        Apply writes the managed config block; rollback strips it. Disabled when the client is not installed on disk.
      </Banner>
      <AsyncBoundary<readonly IntegrationStatus[]>
        state={statuses.state}
        loadingLabel="Loading integrations…"
        empty={<Empty title="No integrations reported." />}
        onRetry={() => statuses.refresh()}
      >
        {(list) => (
          <Stack gap="normal">
            {list.map((status) => (
              <IntegrationCard key={status.id} status={status} onChanged={statuses.refresh} />
            ))}
          </Stack>
        )}
      </AsyncBoundary>
    </Stack>
  )
}
