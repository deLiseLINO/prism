import { app } from 'electron'
import { readFileSync } from 'node:fs'
import { arch, release, type } from 'node:os'
import { join } from 'node:path'
import type { DaemonStatus, ReportContext, ReportSnapshot, ReportTarget } from '@prism/contracts'

const ISSUE_REPO = 'deLiseLINO/prism'
const BODY_LIMIT = 6_000
const WINDOW_LOG_LIMIT = 64 * 1024

export interface ReportSource {
  readonly status: DaemonStatus
  readonly logTail: () => string
  readonly workerUrl: string | null
  readonly version: string
  readonly platform: string
  readonly os: string
  readonly windowLog: () => string
}

export function reportSource(status: DaemonStatus, logTail: () => string, windowLog: () => string = () => windowLogTail()): ReportSource {
  return {
    status,
    logTail,
    workerUrl: workerUrl(),
    version: appVersion(),
    platform: `${process.platform} ${process.arch}`,
    os: `${type()} ${release()} ${arch()}`,
    windowLog,
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

export function buildSnapshot(source: ReportSource, title: string, detail: string, context: ReportContext = { steps: [], failed: null }): ReportSnapshot {
  const body = publicBody(source, title, detail, context)
  return {
    title,
    body,
    issueUrl: issueUrl(title, body),
    workerConfigured: source.workerUrl !== null,
    pageUrl: null,
  }
}

export function issueUrl(title: string, body: string): string {
  const url = new URL(`https://github.com/${ISSUE_REPO}/issues/new`)
  url.searchParams.set('title', title)
  url.searchParams.set('body', body)
  return url.toString()
}

export function publicBody(source: ReportSource, title: string, detail: string, context: ReportContext): string {
  const status = source.status
  const osLine = `os: ${source.os}`
  const request = context.failed === null ? null : `request: ${context.failed.method} ${context.failed.path} ${context.failed.status} ${context.failed.code}`
  const head = [
    title,
    '',
    `version: ${source.version}`,
    `platform: ${source.platform}`,
  ]
  const tail = [
    `daemon: ${status.state}`,
    `endpoint: ${status.endpoint ?? 'none'}`,
    `pid: ${status.pid ?? 'none'}`,
    `attempt: ${status.attempt}`,
    `started: ${status.startedAt ?? 'none'}`,
    `last error: ${status.lastError ?? 'none'}`,
    `last exit: ${exitText(status)}`,
  ]
  const detailLine = `detail: ${detail.replace(/[\r\n]+/g, ' ')}`
  let kept = context.steps
  let detailText = detailLine
  let body = assemble(head, detailText, tail, osLine, kept, request)
  if (body.length > BODY_LIMIT) {
    const emptyDetail = assemble(head, '', tail, osLine, kept, request)
    detailText = detailLine.slice(0, Math.max(0, BODY_LIMIT - emptyDetail.length - 1))
    body = assemble(head, detailText, tail, osLine, kept, request)
  }
  while (body.length > BODY_LIMIT && kept.length > 0) {
    kept = kept.slice(1)
    body = assemble(head, detailText, tail, osLine, kept, request)
  }
  return body
}

function assemble(head: readonly string[], detail: string, tail: readonly string[], osLine: string, steps: ReportContext['steps'], request: string | null): string {
  return [...head, detail, ...tail, osLine, ...(steps.length === 0 ? [] : [stepsLine(steps)]), ...(request === null ? [] : [request])].join('\n')
}

function stepsLine(steps: ReportContext['steps']): string {
  return `steps: ${steps.map((step) => `${clock(step.at)} ${step.kind === 'navigate' ? `navigate ${step.view}` : `action ${step.name}`}`).join(' · ')}`
}

function clock(at: number): string {
  const date = new Date(at)
  return [date.getHours(), date.getMinutes(), date.getSeconds()].map((part) => String(part).padStart(2, '0')).join('')
}

function exitText(status: DaemonStatus): string {
  if (status.lastExit === null) return 'none'
  return `code=${status.lastExit.code ?? 'none'} signal=${status.lastExit.signal ?? 'none'}`
}

export async function sendReport(source: ReportSource, target: ReportTarget, title: string, detail: string, screenshot: string | null, context: ReportContext = { steps: [], failed: null }): Promise<string | null> {
  if (source.workerUrl === null) throw new Error('prism: report worker is not configured')
  const snapshot = buildSnapshot(source, title, detail, context)
  const payload = {
    target,
    title: snapshot.title,
    body: snapshot.body,
    ...(target === 'bot' ? { log: botLog(source), screenshot } : {}),
  }
  const response = await fetch(source.workerUrl, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify(payload),
    signal: AbortSignal.timeout(20_000),
  })
  if (!response.ok) throw new Error(`prism: report worker returned ${response.status}`)
  if (target !== 'bot') return null
  const page = await response.json() as { url?: unknown }
  return typeof page.url === 'string' ? page.url : null
}

function botLog(source: ReportSource): string {
  return `${source.logTail()}\n--- window.log\n${source.windowLog().slice(-WINDOW_LOG_LIMIT)}`
}

const windowLines: string[] = []
let windowBytes = 0
let windowLogInstalled = false

export function noteWindowLine(line: string): void {
  const text = line.replace(/[\r\n]+/g, ' ')
  windowLines.push(text)
  windowBytes += text.length + 1
  while (windowLines.length > 1 && windowBytes > WINDOW_LOG_LIMIT) {
    windowBytes -= (windowLines.shift()?.length ?? 0) + 1
  }
}

export function windowLogTail(): string {
  return windowLines.join('\n').slice(-WINDOW_LOG_LIMIT)
}

export function installWindowLog(contents: { on(event: 'console-message', listener: (_event: unknown, level: number, message: string) => void): void }): void {
  if (windowLogInstalled) return
  windowLogInstalled = true
  contents.on('console-message', (_event, _level, message) => noteWindowLine(message))
  const write = console.error
  console.error = (...args: unknown[]) => {
    noteWindowLine(args.map(String).join(' '))
    write.apply(console, args)
  }
}
