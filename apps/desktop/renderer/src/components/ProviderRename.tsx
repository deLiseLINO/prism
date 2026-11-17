import { useEffect, useRef, useState } from 'react'
import type { ProviderRenameResponse, RenameConflictBody } from '@prism/contracts'
import { Banner } from './Ui'
import { DialogSection } from './Patterns'
import { describeError } from '../useAsync'
import { api, type RenameOutcome } from '../api'

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

export interface RenameDraft {
  readonly to: string
  readonly renaming: boolean
  readonly problem: string | null
  readonly preview: RenamePreview
  readonly ready: boolean
  // Records conflicts the daemon returned for the real rename, after a clean dry run.
  readonly blockedOnCommit: (conflicts: RenameConflictBody['conflicts']) => void
}

// useRenameDraft dry-runs every changed id against the daemon after a short pause.
export function useRenameDraft(from: string | null, draft: string, generation: number): RenameDraft {
  const [preview, setPreview] = useState<RenamePreview>({ kind: 'idle' })
  const sequence = useRef(0)
  const to = draft.trim()
  const renaming = from !== null && to !== '' && to !== from
  const problem = from === null ? null : renameDraftProblem(from, draft)
  const checkable = renaming && problem === null

  useEffect(() => {
    const ticket = ++sequence.current
    if (from === null || !checkable) {
      setPreview({ kind: 'idle' })
      return
    }
    setPreview({ kind: 'checking', to })
    const timer = setTimeout(() => {
      api
        .renameProvider(from, { to, expectedGeneration: generation, dryRun: true })
        .then((outcome) => {
          if (ticket === sequence.current) setPreview(previewFromOutcome(to, outcome))
        })
        .catch((err: unknown) => {
          if (ticket === sequence.current) setPreview({ kind: 'failed', to, error: err instanceof Error ? err : new Error(String(err)) })
        })
    }, PREVIEW_DELAY_MS)
    return () => clearTimeout(timer)
  }, [from, generation, to, checkable])

  return {
    to,
    renaming,
    problem,
    preview,
    ready: preview.kind === 'ready' && preview.to === to,
    blockedOnCommit: (conflicts) => setPreview({ kind: 'blocked', to, conflicts }),
  }
}

export function RenameChanges({ from, rename }: { readonly from: string; readonly rename: RenameDraft }): JSX.Element {
  const { preview, to } = rename
  const current = preview.kind !== 'idle' && preview.to === to
  return (
    <DialogSection label="What the new id changes" meta={preview.kind === 'checking' ? 'checking…' : undefined}>
      {current && preview.kind === 'blocked' ? (
        <Banner tone="error" title="Rename blocked, nothing changed">
          {preview.conflicts.map((c) => (
            <span key={c.kind + c.detail} className="rename-issue">{c.detail}</span>
          ))}
        </Banner>
      ) : current && preview.kind === 'failed' ? (
        <Banner tone="error" title="Check failed">{describeError(preview.error)}</Banner>
      ) : current && preview.kind === 'ready' ? (
        <RenamePlan from={from} preview={preview.response} />
      ) : (
        <p className="field__hint">Checking the new id with the daemon…</p>
      )}
    </DialogSection>
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
