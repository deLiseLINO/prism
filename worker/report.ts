interface Env {
  readonly REPORTS: KVNamespace
  readonly REPORT_PAGE_KEY: string
  readonly TELEGRAM_BOT_TOKEN: string
  readonly TELEGRAM_CHAT_ID: string
  readonly GITHUB_TOKEN?: string
  readonly GITHUB_REPO?: string
}

interface StoredReport {
  readonly title: string
  readonly facts: readonly [string, string][]
  readonly detail: string
  readonly log: string
  readonly screenshot: string
  readonly createdAt: string
}

interface IncomingReport {
  readonly target: 'issue' | 'bot'
  readonly title: string
  readonly body: string
  readonly log: string
  readonly screenshot: string
}

function incoming(value: unknown): IncomingReport | null {
  if (typeof value !== 'object' || value === null) return null
  if (!('target' in value) || !('title' in value) || !('body' in value)) return null
  const target = value.target
  const title = value.title
  const body = value.body
  const log = 'log' in value ? value.log : ''
  const screenshot = 'screenshot' in value ? value.screenshot : ''
  if (target !== 'issue' && target !== 'bot') return null
  if (typeof title !== 'string' || typeof body !== 'string') return null
  if (typeof log !== 'string' || typeof screenshot !== 'string') return null
  return { target, title, body, log, screenshot }
}

function storedReport(raw: string): StoredReport | null {
  const value: unknown = JSON.parse(raw)
  if (typeof value !== 'object' || value === null) return null
  if (!('title' in value) || !('detail' in value) || !('log' in value) || !('screenshot' in value) || !('createdAt' in value) || !('facts' in value)) return null
  const title = value.title
  const detailText = value.detail
  const log = value.log
  const screenshot = value.screenshot
  const createdAt = value.createdAt
  const rows = value.facts
  if (typeof title !== 'string' || typeof detailText !== 'string' || typeof log !== 'string' || typeof screenshot !== 'string' || typeof createdAt !== 'string') return null
  if (!Array.isArray(rows)) return null
  const facts: [string, string][] = []
  for (const row of rows) {
    if (!Array.isArray(row) || row.length !== 2) return null
    if (typeof row[0] !== 'string' || typeof row[1] !== 'string') return null
    facts.push([row[0], row[1]])
  }
  return { title, facts, detail: detailText, log, screenshot, createdAt }
}

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    const url = new URL(request.url)
    if (request.method === 'GET') return readPage(env, url)
    if (request.method !== 'POST') return text('method not allowed', 405)
    const report = incoming(await request.json())
    if (report === null) return text('missing report', 400)
    try {
      if (report.target === 'issue') {
        await postIssue(env, report.title, report.body)
        return new Response(null, { status: 204 })
      }
      const page = await storePage(env, url.origin, report)
      await postBot(env, report, page)
      return Response.json({ url: page })
    } catch (error) {
      return text(error instanceof Error ? error.message : String(error), 502)
    }
  },
}

async function readPage(env: Env, url: URL): Promise<Response> {
  if (url.searchParams.get('key') !== env.REPORT_PAGE_KEY) return text('not found', 404)
  const id = url.pathname.slice(1)
  if (!/^[a-z0-9]{16}$/.test(id)) return text('not found', 404)
  const stored = await env.REPORTS.get(id)
  if (stored === null) return text('not found', 404)
  const report = storedReport(stored)
  if (report === null) return text('not found', 404)
  if (url.searchParams.get('format') === 'json') return Response.json(report)
  return new Response(pageHtml(report), { headers: { 'content-type': 'text/html; charset=utf-8' } })
}

async function storePage(env: Env, origin: string, report: IncomingReport): Promise<string> {
  const id = crypto.randomUUID().replaceAll('-', '').slice(0, 16)
  const body = report.body
  const stored: StoredReport = {
    title: report.title,
    facts: facts(body),
    detail: detail(body),
    log: report.log,
    screenshot: report.screenshot,
    createdAt: new Date().toISOString(),
  }
  await env.REPORTS.put(id, JSON.stringify(stored))
  return `${origin}/${id}?key=${encodeURIComponent(env.REPORT_PAGE_KEY)}`
}

function facts(body: string): readonly [string, string][] {
  return body.split('\n').flatMap((line) => {
    const split = line.indexOf(': ')
    if (split < 1) return []
    return [[line.slice(0, split), line.slice(split + 2)] as [string, string]]
  })
}

function detail(body: string): string {
  return body.split('\n').find((line) => line.startsWith('detail: '))?.slice(8) ?? ''
}

function pageHtml(report: StoredReport): string {
  const shot = report.screenshot.startsWith('data:image/png;base64,')
    ? `<figure class="shot"><img alt="Prism window" src="${escapeAttr(report.screenshot)}"></figure>`
    : `<figure class="shot shot--empty"><p>No window capture.</p></figure>`
  const rows = report.facts
    .filter(([name]) => name !== 'detail')
    .map(([name, value]) => `<div><dt>${escapeHtml(name)}</dt><dd>${escapeHtml(value)}</dd></div>`)
    .join('')
  const when = new Date(report.createdAt).toLocaleString('en', {
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  })
  const log = report.log === '' ? 'No daemon log.' : report.log
  return `<!doctype html>
<html lang="en">
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="dark">
<title>${escapeHtml(report.title)}</title>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=JetBrains+Mono:wght@450&family=Outfit:wght@450;560;640&display=swap" rel="stylesheet">
<style>
  :root {
    color-scheme: dark;
    --canvas: #10151b;
    --surface: #1b222b;
    --fg: #e9edf2;
    --muted: #8d99a7;
    --line: #2b3440;
    --accent: #6f9cc4;
    --danger: #d3898f;
  }
  * { box-sizing: border-box; }
  body {
    margin: 0;
    min-height: 100dvh;
    background:
      radial-gradient(640px 280px at 12% -8%, rgba(111, 156, 196, 0.12), transparent 70%),
      var(--canvas);
    color: var(--fg);
    font: 450 15px/1.45 Outfit, ui-sans-serif, system-ui, sans-serif;
  }
  main { width: min(1180px, calc(100% - 48px)); margin: 28px auto 56px; }
  .top { display: flex; align-items: center; justify-content: space-between; gap: 16px; margin-bottom: 22px; }
  .mark { display: flex; align-items: center; gap: 10px; color: var(--muted); font-size: 13px; letter-spacing: 0.04em; }
  .mark svg { color: var(--accent); }
  time { color: var(--muted); font-family: "JetBrains Mono", ui-monospace, monospace; font-size: 12px; }
  h1 { margin: 0 0 8px; font-size: 32px; font-weight: 640; letter-spacing: -0.03em; line-height: 1.1; }
  .detail { margin: 0 0 22px; max-width: 68ch; color: var(--danger); font-size: 15px; }
  .stage { display: grid; grid-template-columns: minmax(280px, 0.82fr) minmax(0, 1.18fr); gap: 18px; align-items: start; }
  .facts, .shot, .log {
    background: var(--surface);
    border: 1px solid var(--line);
    border-radius: 16px;
    box-shadow: inset 0 1px 0 rgba(148, 163, 184, 0.08);
  }
  .facts { margin: 0; padding: 8px 18px 14px; }
  .facts div { display: grid; grid-template-columns: 92px minmax(0, 1fr); gap: 12px; padding: 11px 0; border-top: 1px solid var(--line); }
  .facts div:first-child { border-top: 0; }
  dt { color: var(--muted); font-size: 12px; text-transform: uppercase; letter-spacing: 0.06em; padding-top: 2px; }
  dd { margin: 0; overflow-wrap: anywhere; font-family: "JetBrains Mono", ui-monospace, monospace; font-size: 13px; }
  .shot { margin: 0; overflow: hidden; min-height: 180px; }
  .shot img { display: block; width: 100%; height: auto; }
  .shot--empty { display: grid; place-items: center; color: var(--muted); }
  .log { margin-top: 18px; }
  .log h2 { margin: 0; padding: 14px 18px 0; font-size: 12px; font-weight: 560; letter-spacing: 0.06em; text-transform: uppercase; color: var(--muted); }
  pre { margin: 0; padding: 12px 18px 16px; white-space: pre-wrap; overflow-wrap: anywhere; font: 12.5px/1.6 "JetBrains Mono", ui-monospace, monospace; color: #c0c9d3; }
  @media (max-width: 860px) {
    main { width: min(100% - 28px, 1180px); }
    .stage { grid-template-columns: 1fr; }
    h1 { font-size: 26px; }
  }
</style>
<main>
  <div class="top">
    <div class="mark">
      <svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true"><path fill="currentColor" d="M2 3.2 8 1l6 2.2v4.7c0 3.3-2.4 5.7-6 6.9-3.6-1.2-6-3.6-6-6.9z"/></svg>
      Prism report
    </div>
    <time datetime="${escapeAttr(report.createdAt)}">${escapeHtml(when)}</time>
  </div>
  <h1>${escapeHtml(report.title)}</h1>
  <p class="detail">${escapeHtml(report.detail || 'No extra detail.')}</p>
  <section class="stage">
    <dl class="facts">${rows}</dl>
    ${shot}
  </section>
  <section class="log">
    <h2>Daemon log</h2>
    <pre>${escapeHtml(log)}</pre>
  </section>
</main>`
}

function escapeHtml(value: string): string {
  return value.replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;')
}

function escapeAttr(value: string): string {
  return escapeHtml(value).replaceAll('"', '&quot;')
}

async function postIssue(env: Env, title: string, body: string): Promise<void> {
  if (env.GITHUB_TOKEN === undefined || env.GITHUB_REPO === undefined) throw new Error('github is not configured')
  const response = await fetch(`https://api.github.com/repos/${env.GITHUB_REPO}/issues`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${env.GITHUB_TOKEN}`,
      accept: 'application/vnd.github+json',
      'user-agent': 'prism-report',
      'content-type': 'application/json',
    },
    body: JSON.stringify({ title: title.slice(0, 200), body }),
  })
  if (!response.ok) throw new Error(`github ${response.status}`)
}

async function postBot(env: Env, report: IncomingReport, page: string): Promise<void> {
  const meta = [fact(report.body, 'version'), fact(report.body, 'platform')].filter((line) => line !== '').join(' · ')
  const caption = [
    `<b>${escapeHtml(report.title)}</b>`,
    meta === '' ? '' : `<i>${escapeHtml(meta)}</i>`,
    '',
    escapeHtml(detail(report.body)),
    '',
    `<a href="${escapeAttr(page)}">Open report</a>`,
  ].filter((line, index, all) => line !== '' || (index > 0 && all[index - 1] !== '')).join('\n').slice(0, 1000)
  const photo = png(report.screenshot)
  const response = photo === null
    ? await fetch(`https://api.telegram.org/bot${env.TELEGRAM_BOT_TOKEN}/sendMessage`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ chat_id: env.TELEGRAM_CHAT_ID, text: caption, parse_mode: 'HTML', link_preview_options: { is_disabled: true } }),
    })
    : await fetch(`https://api.telegram.org/bot${env.TELEGRAM_BOT_TOKEN}/sendPhoto`, {
      method: 'POST',
      body: photoBody(env.TELEGRAM_CHAT_ID, caption, photo),
    })
  if (!response.ok) throw new Error(await telegramError(response))
}

function png(value: string): Uint8Array | null {
  const prefix = 'data:image/png;base64,'
  if (!value.startsWith(prefix)) return null
  const bytes = Uint8Array.from(atob(value.slice(prefix.length)), (char) => char.charCodeAt(0))
  return bytes.byteLength === 0 ? null : bytes
}

function photoBody(chatId: string, caption: string, photo: Uint8Array): FormData {
  const body = new FormData()
  body.set('chat_id', chatId)
  body.set('caption', caption)
  body.set('parse_mode', 'HTML')
  body.set('photo', new File([photo], 'prism.png', { type: 'image/png' }))
  return body
}

function fact(body: string, name: string): string {
  return body.split('\n').find((line) => line.startsWith(`${name}: `))?.slice(name.length + 2) ?? ''
}

async function telegramError(response: Response): Promise<string> {
  const body = await response.text()
  return `telegram ${response.status} ${body.replace(/bot\d+:[A-Za-z0-9_-]+/g, 'bot<redacted>')}`
}

function text(message: string, status: number): Response {
  return new Response(message, { status })
}
