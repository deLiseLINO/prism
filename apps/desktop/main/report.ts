import { app } from 'electron'
import { execFileSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import type { DaemonStatus, ReportSnapshot, ReportTarget } from '@prism/contracts'

const ISSUE_REPO = 'deLiseLINO/prism'
const BODY_LIMIT = 6_000
const LOG_LIMIT = 12_000

export interface ReportSource {
  readonly status: DaemonStatus
  readonly logTail: () => string
  readonly workerUrl: string | null
  readonly version: string
  readonly platform: string
}

export function reportSource(status: DaemonStatus, logTail: () => string): ReportSource {
  return {
    status,
    logTail,
    workerUrl: workerUrl(),
    version: appVersion(),
    platform: `${process.platform} ${process.arch}`,
  }
}

function workerUrl(): string | null {
  const raw = process.env.PRISM_REPORT_URL
  if (raw === undefined || raw === '') return null
  try {
    const url = new URL(raw)
    if (url.protocol !== 'https:') return null
    return url.toString()
  } catch {
    return null
  }
}

function appVersion(): string {
  const base = packagedVersion()
  if (/-rc\.[0-9]+$/.test(base) || /-beta\.[0-9]+$/.test(base)) return base
  const channel = process.env.PRISM_REPORT_CHANNEL
  if (channel === 'rc' || channel === 'beta') return `${base}-${channel}`
  return base
}

function packagedVersion(): string {
  if (app.isPackaged) return app.getVersion()
  try {
    const pkg = JSON.parse(readFileSync(join(app.getAppPath(), 'package.json'), 'utf8')) as { version?: string }
    return pkg.version ?? app.getVersion()
  } catch {
    return app.getVersion()
  }
}

export function buildSnapshot(source: ReportSource, title: string, detail: string): ReportSnapshot {
  const body = publicBody(source, title, detail)
  return {
    title,
    body,
    issueUrl: issueUrl(title, body),
    workerConfigured: source.workerUrl !== null,
  }
}

export function issueUrl(title: string, body: string): string {
  const url = new URL(`https://github.com/${ISSUE_REPO}/issues/new`)
  url.searchParams.set('title', title)
  url.searchParams.set('body', body)
  return url.toString()
}

function publicBody(source: ReportSource, title: string, detail: string): string {
  const status = source.status
  const lines = [
    title,
    '',
    `version: ${source.version}`,
    `platform: ${source.platform}`,
    `detail: ${detail}`,
    `daemon: ${status.state}`,
    `endpoint: ${status.endpoint ?? 'none'}`,
    `pid: ${status.pid ?? 'none'}`,
    `attempt: ${status.attempt}`,
    `started: ${status.startedAt ?? 'none'}`,
    `last error: ${status.lastError ?? 'none'}`,
    `last exit: ${exitText(status)}`,
  ]
  return lines.join('\n').slice(0, BODY_LIMIT)
}

function exitText(status: DaemonStatus): string {
  if (status.lastExit === null) return 'none'
  return `code=${status.lastExit.code ?? 'none'} signal=${status.lastExit.signal ?? 'none'}`
}

export async function sendReport(source: ReportSource, target: ReportTarget, title: string, detail: string): Promise<void> {
  if (source.workerUrl === null) throw new Error('prism: report worker is not configured')
  const snapshot = buildSnapshot(source, title, detail)
  const payload = {
    target,
    title: snapshot.title,
    body: snapshot.body,
    ...(target === 'bot' ? { log: source.logTail().slice(-LOG_LIMIT) } : {}),
  }
  const response = await fetch(source.workerUrl, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify(payload),
    signal: AbortSignal.timeout(10_000),
  })
  if (!response.ok) throw new Error(`prism: report worker returned ${response.status}`)
}
