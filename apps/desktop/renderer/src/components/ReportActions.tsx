import { useEffect, useState } from 'react'
import { bridge } from '../bridge'
import { Button } from './Ui'

const GITHUB_REPORTS = false
const ISSUE_REPO = 'deLiseLINO/prism'

export function ReportActions({ title, detail }: { readonly title: string; readonly detail: string }): JSX.Element {
  const [note, setNote] = useState<string | null>(null)
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
    await bridge.report.send(target, title, detail)
    setNote(target === 'bot' ? 'Reported.' : 'Sent as an anonymous issue.')
  }

  return (
    <div className="banner__action">
      {GITHUB_REPORTS ? <Button tone="ghost" size="sm" onClick={() => void run(ownIssue, setNote)}>Open issue</Button> : null}
      {GITHUB_REPORTS && worker ? <Button tone="ghost" size="sm" onClick={() => void run(() => send('issue'), setNote)}>Send anonymously</Button> : null}
      {worker ? <Button tone="ghost" size="sm" onClick={() => void run(() => send('bot'), setNote)}>Report anonymously</Button> : null}
      {note !== null ? <span className="note">{note}</span> : null}
    </div>
  )
}

async function run(action: () => Promise<void>, setNote: (note: string) => void): Promise<void> {
  try {
    await action()
  } catch (error) {
    setNote(error instanceof Error ? error.message : String(error))
  }
}
