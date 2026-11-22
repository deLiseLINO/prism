import { useEffect, useState } from 'react'
import type { ConnectPlan, HostId } from '@prism/contracts'
import { Banner, Button } from './Ui'
import { Dialog, DialogSection } from './Patterns'
import { useTask, describeError } from '../useAsync'
import { ApiError, api } from '../api'
import { wireLabel } from '../wires'

export interface ConnectEndpointRow {
  readonly url: string
  readonly primary: boolean
}

export type ConnectPlanRow =
  | { readonly kind: 'text'; readonly label: string; readonly value: string }
  | { readonly kind: 'endpoints'; readonly label: string; readonly endpoints: readonly ConnectEndpointRow[] }

export function connectEndpointRows(plan: Pick<ConnectPlan, 'baseURL' | 'fallbackBaseURLs'>): readonly ConnectEndpointRow[] {
  return [plan.baseURL, ...plan.fallbackBaseURLs].map((url, index) => ({ url, primary: index === 0 }))
}

export function connectPlanRows(plan: ConnectPlan): readonly ConnectPlanRow[] {
  return [
    { kind: 'text', label: 'ID', value: plan.id },
    { kind: 'text', label: 'Wire', value: wireLabel(plan.wire) },
    { kind: 'endpoints', label: plan.fallbackBaseURLs.length === 0 ? 'Endpoint' : 'Endpoints', endpoints: connectEndpointRows(plan) },
    { kind: 'text', label: 'Mode', value: plan.endpointMode === 'spread' ? 'spread' : 'failover' },
    { kind: 'text', label: 'Models', value: plan.models.length === 0 ? 'discovered after adding' : plan.models.join(', ') },
    ...(plan.defaultModel === '' ? [] : [{ kind: 'text' as const, label: 'Default model', value: plan.defaultModel }]),
    { kind: 'text', label: 'Key', value: plan.hasKey ? plan.keyPreview : 'none' },
  ]
}

export function connectPlanProblem(plan: Pick<ConnectPlan, 'id' | 'exists'>): string | null {
  return plan.exists ? `A provider with the id ${plan.id} already exists. A link never replaces a provider.` : null
}

export function ConnectPlanBody({ plan }: { readonly plan: ConnectPlan }): JSX.Element {
  return (
    <div className="kv">
      {connectPlanRows(plan).map((row) => (
        <div className="kv-row" key={row.label}>
          <span className="kv-k">{row.label}</span>
          {row.kind === 'text' ? (
            <span className="kv-v">{row.value}</span>
          ) : (
            <span className="kv-v connect-endpoints">
              {row.endpoints.map((endpoint) => (
                <span key={endpoint.url} className="connect-endpoint">
                  {endpoint.primary ? <span className="badge badge--info">primary</span> : null}
                  <span>{endpoint.url}</span>
                </span>
              ))}
            </span>
          )}
        </div>
      ))}
    </div>
  )
}

export interface ConnectLinkDialogProps {
  readonly link: string
  // Machine whose daemon receives the provider and its key.
  readonly host: HostId
  readonly onCancel: () => void
  readonly onAdded: (providerId: string) => void
  // True while the provider is being added, so the owner can hold a newer link instead of replacing this dialog.
  readonly onBusyChange: (busy: boolean) => void
}

export function ConnectLinkDialog({ link, host, onCancel, onAdded, onBusyChange }: ConnectLinkDialogProps): JSX.Element {
  const preview = useTask()
  const apply = useTask()
  const [plan, setPlan] = useState<ConnectPlan | null>(null)
  const [saved, setSaved] = useState<{ readonly id: string; readonly warning: string } | null>(null)

  useEffect(() => {
    void preview.run(() => api.connect(host, link, true)).then((response) => {
      if (response !== undefined) setPlan(response.plan)
    })
  }, [host, link])

  useEffect(() => {
    onBusyChange(apply.running)
    return () => onBusyChange(false)
  }, [apply.running, onBusyChange])

  const problem = plan === null ? null : connectPlanProblem(plan)
  const blocked = plan === null || problem !== null || preview.error !== null

  async function confirm(): Promise<void> {
    if (blocked) return
    const response = await apply.run(() => api.connect(host, link, false))
    if (response === undefined) return
    const id = response.provider?.id ?? response.plan.id
    if (response.warning !== '') {
      setSaved({ id, warning: response.warning })
      return
    }
    onAdded(id)
  }

  const close = saved === null ? onCancel : () => onAdded(saved.id)

  return (
    <Dialog
      label="Add provider from link"
      title="Add provider from link"
      subtitle={plan === null ? undefined : <span className="badge badge--muted num">{wireLabel(plan.wire)}</span>}
      onClose={close}
      footer={
        saved === null ? (
          <>
            <span className="ctl-spacer" />
            <Button tone="ghost" onClick={onCancel} disabled={apply.running}>
              Cancel
            </Button>
            <Button tone="primary" onClick={() => void confirm()} disabled={blocked || apply.running} busy={apply.running}>
              Add provider
            </Button>
          </>
        ) : (
          <>
            <span className="ctl-spacer" />
            <Button tone="primary" onClick={close}>
              Done
            </Button>
          </>
        )
      }
      error={
        preview.error !== null ? (
          <Banner tone="error" title={preview.error instanceof ApiError && preview.error.code === 'invalid_link' ? 'This link is not valid' : 'Link check failed'}>
            {describeError(preview.error)}
          </Banner>
        ) : problem !== null ? (
          <Banner tone="error" title="Provider already exists">{problem}</Banner>
        ) : apply.error !== null ? (
          <Banner tone="error" title={apply.error instanceof ApiError && apply.error.code === 'already_exists' ? 'Provider already exists' : 'Add failed'}>
            {describeError(apply.error)}
          </Banner>
        ) : undefined
      }
    >
      {host === 'local' ? null : <p className="field__hint">The provider will be added to {host}.</p>}
      <DialogSection label="Provider">
        {plan !== null ? (
          <ConnectPlanBody plan={plan} />
        ) : preview.error === null ? (
          <p className="field__hint">Checking the link with the daemon…</p>
        ) : (
          <p className="field__hint">Nothing was changed.</p>
        )}
      </DialogSection>
      {saved !== null ? (
        <DialogSection>
          <Banner tone="warn" title={`Provider ${saved.id} saved`}>{saved.warning}</Banner>
        </DialogSection>
      ) : null}
    </Dialog>
  )
}
