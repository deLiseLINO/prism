import { useEffect, useRef, useState } from 'react'
import type { ProviderRenameResponse, ProviderView, RenameConflictBody } from '@prism/contracts'
import { Banner, Button, Field, TextInput } from './Ui'
import { Dialog, DialogSection } from './Patterns'
import { describeError, useTask } from '../useAsync'
import { ApiError, api, type RenameOutcome } from '../api'
import { wireLabel } from '../wires'

const ID_PATTERN = /^[A-Za-z0-9_-]*[A-Za-z0-9][A-Za-z0-9_-]*$/
const PREVIEW_DELAY_MS = 250

// The daemon's dry-run answer for one draft id.
export type RenamePreview =
  | { readonly kind: 'idle' }
  | { readonly kind: 'checking'; readonly to: string }
  | { readonly kind: 'ready'; readonly to: string; readonly response: ProviderRenameResponse }
  | { readonly kind: 'blocked'; readonly to: string; readonly conflicts: RenameConflictBody['conflicts'] }
  | { readonly kind: 'failed'; readonly to: string; readonly error: Error }

export function renameDraftProblem(from: string, draft: string): string | null {
  const to = draft.trim()
  if (to === '' || to === from) return null
  if (!ID_PATTERN.test(to)) return "Use letters, digits, '_' and '-'."
  return null
}

export function previewFromOutcome(to: string, outcome: RenameOutcome): RenamePreview {
  return outcome.kind === 'ok'
    ? { kind: 'ready', to, response: outcome.response }
    : { kind: 'blocked', to, conflicts: outcome.conflicts }
}

export interface RenameProviderModalProps {
  readonly provider: ProviderView
  readonly generation: number
  readonly onCancel: () => void
  readonly onRenamed: (to: string) => void
}

export function RenameProviderModal({ provider, generation, onCancel, onRenamed }: RenameProviderModalProps): JSX.Element {
  const [draft, setDraft] = useState(provider.id)
  const [preview, setPreview] = useState<RenamePreview>({ kind: 'idle' })
  const [blockedOnCommit, setBlockedOnCommit] = useState<RenameConflictBody['conflicts'] | null>(null)
  const task = useTask()
  const sequence = useRef(0)
  const to = draft.trim()
  const problem = renameDraftProblem(provider.id, draft)
  const checkable = to !== '' && to !== provider.id && problem === null

  useEffect(() => {
    const ticket = ++sequence.current
    setBlockedOnCommit(null)
    if (!checkable) {
      setPreview({ kind: 'idle' })
      return
    }
    setPreview({ kind: 'checking', to })
    const timer = setTimeout(() => {
      api
        .renameProvider(provider.id, { to, expectedGeneration: generation, dryRun: true })
        .then((outcome) => {
          if (ticket === sequence.current) setPreview(previewFromOutcome(to, outcome))
        })
        .catch((err: unknown) => {
          if (ticket === sequence.current) setPreview({ kind: 'failed', to, error: err instanceof Error ? err : new Error(String(err)) })
        })
    }, PREVIEW_DELAY_MS)
    return () => clearTimeout(timer)
  }, [provider.id, generation, to, checkable])

  const ready = preview.kind === 'ready' && preview.to === to && blockedOnCommit === null

  async function rename(): Promise<void> {
    if (!ready) return
    const outcome = await task.run(() => api.renameProvider(provider.id, { to, expectedGeneration: generation }))
    if (outcome === undefined) return
    if (outcome.kind === 'blocked') {
      setBlockedOnCommit(outcome.conflicts)
      return
    }
    onRenamed(to)
  }

  const conflicts = blockedOnCommit ?? (preview.kind === 'blocked' && preview.to === to ? preview.conflicts : null)

  return (
    <Dialog
      label={'Rename provider ' + provider.id}
      title={<span className="num">Rename {provider.id}</span>}
      subtitle={<span className="badge badge--muted num">{wireLabel(provider.wire)}</span>}
      onClose={onCancel}
      footer={
        <>
          <span className="ctl-spacer" />
          <Button tone="ghost" onClick={onCancel} disabled={task.running}>
            Cancel
          </Button>
          <Button tone="primary" onClick={() => void rename()} disabled={!ready || task.running} busy={task.running}>
            Rename
          </Button>
        </>
      }
      error={
        task.error !== null ? (
          <Banner tone="error" title={task.error instanceof ApiError && task.error.code === 'rename_incomplete' ? 'Rename saved, data still moving' : 'Rename failed'}>
            {describeError(task.error)}
          </Banner>
        ) : undefined
      }
    >
      <DialogSection label="New id">
        <Field
          label="ID"
          htmlFor="prov-rename-to"
          hint={problem ?? 'Clients see this provider’s models as <id>/<model>.'}
        >
          <TextInput id="prov-rename-to" value={draft} onChange={setDraft} autoComplete="off" spellCheck={false} />
        </Field>
      </DialogSection>
      {conflicts !== null ? (
        <Banner tone="error" title="Rename blocked, nothing changed">
          {conflicts.map((c) => (
            <span key={c.kind + c.detail} className="rename-issue">{c.detail}</span>
          ))}
        </Banner>
      ) : null}
      {preview.kind === 'failed' && preview.to === to ? (
        <Banner tone="error" title="Check failed">{describeError(preview.error)}</Banner>
      ) : null}
      <DialogSection label="What changes" meta={preview.kind === 'checking' ? 'checking…' : undefined}>
        {preview.kind === 'ready' && preview.to === to ? (
          <RenamePlan from={provider.id} preview={preview.response} />
        ) : (
          <p className="field__hint">
            {checkable ? 'Checking the new id with the daemon…' : 'Type a new id to see what the rename touches.'}
          </p>
        )}
      </DialogSection>
    </Dialog>
  )
}

function RenamePlan({ from, preview }: { readonly from: string; readonly preview: ProviderRenameResponse }): JSX.Element {
  const accounts = preview.plan.accounts
  return (
    <>
      <div className="kv">
        <div className="kv-row">
          <span className="kv-k">Model ids</span>
          <span className="kv-v">{from}/&lt;model&gt; → {preview.to}/&lt;model&gt;</span>
        </div>
        <div className="kv-row">
          <span className="kv-k">Config references</span>
          <span className="kv-v">{preview.plan.configRefs}</span>
        </div>
        <div className="kv-row">
          <span className="kv-k">Accounts moved</span>
          <span className="kv-v">{accounts.length === 0 ? 'none' : accounts.join(', ')}</span>
        </div>
        <div className="kv-row">
          <span className="kv-k">Usage history</span>
          <span className="kv-v">{preview.plan.usageAttempts} attempts</span>
        </div>
      </div>
      {(preview.warnings ?? []).map((w) => (
        <p key={w.kind} className="field__hint rename-note">{w.detail}</p>
      ))}
    </>
  )
}
