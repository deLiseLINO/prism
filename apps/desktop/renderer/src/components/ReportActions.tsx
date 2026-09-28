import { useEffect, useState } from 'react'
import { bridge } from '../bridge'
import { reportContext } from '../diagnostics'

import { Button } from './Ui'

const GITHUB_REPORTS = false
const ISSUE_REPO = 'deLiseLINO/prism'

export function ReportActions({ title, detail }: { readonly title: string; readonly detail: string }): JSX.Element {
  const [state, setState] = useState<'idle' | 'sending' | 'sent'>('idle')
  const [failure, setFailure] = useState<string | null>(null)
  const [worker, setWorker] = useState(false)

  useEffect(() => {
    void bridge.report.snapshot(title, detail).then((snapshot) => setWorker(snapshot.workerConfigured)).catch(() => setWorker(false))
  }, [title, detail])

  async function ownIssue(): Promise<void> {
    const snapshot = await bridge.report.snapshot(title, detail)
    const url = new URL(`https://github.com/${ISSUE_REPO}/issues/new`)
    url.searchParams.set('title', snapshot.title)
    url.searchParams.set('body', snapshot.body)
    await bridge.shell.openExternal(url.toString())
  }

  async function send(target: 'issue' | 'bot'): Promise<void> {
    setState('sending')
    setFailure(null)
    try {
      const page = await bridge.report.send(target, title, detail, null, reportContext())
      setState('sent')
      if (page !== null) await bridge.shell.openExternal(page)
    } catch (error) {
      setState('idle')
      throw error
    }
  }

  return (
    <div className="report-actions">
      {GITHUB_REPORTS ? <Button tone="ghost" size="sm" onClick={() => void run(ownIssue, setFailure)}>Open issue</Button> : null}
      {GITHUB_REPORTS && worker ? <Button tone="ghost" size="sm" onClick={() => void run(() => send('issue'), setFailure)}>Send anonymously</Button> : null}
      {worker && state !== 'sent' ? <Button tone="ghost" size="sm" disabled={state === 'sending'} onClick={() => void run(() => send('bot'), setFailure)}>{state === 'sending' ? 'Sending' : 'Report'}</Button> : null}
      {state === 'sent' ? <span className="report-actions__sent">Sent</span> : null}
      {failure !== null ? <p className="report-actions__fail">{failure}</p> : null}
    </div>
  )
}

async function run(action: () => Promise<void>, setFailure: (note: string | null) => void): Promise<void> {
  try {
    await action()
  } catch (error) {
    setFailure(error instanceof Error ? error.message : String(error))
  }
}

