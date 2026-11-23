// Serves https://<host>/connect#<parameters>, the web form of a connect link.
// The parameters, key included, stay in the fragment: the browser never sends
// it, so this worker and link previews only ever see /connect. The page's own
// script turns the fragment into prism://connect?<parameters> for the app.

const REPO = 'deLiseLINO/prism'
const RELEASES_PAGE = `https://github.com/${REPO}/releases`

interface Download {
  readonly label: string
  readonly os: 'mac' | 'windows' | 'linux'
  readonly pattern: RegExp
}

const DOWNLOADS: readonly Download[] = [
  { label: 'macOS · Apple Silicon', os: 'mac', pattern: /^Prism-.+-arm64\.dmg$/ },
  { label: 'macOS · Intel', os: 'mac', pattern: /^Prism-.+-x64\.dmg$/ },
  { label: 'Windows', os: 'windows', pattern: /^Prism-.+-setup\.exe$/ },
  { label: 'Linux · x86_64 AppImage', os: 'linux', pattern: /^Prism-.+-x86_64\.AppImage$/ },
  { label: 'Linux · arm64 AppImage', os: 'linux', pattern: /^Prism-.+-arm64\.AppImage$/ },
]

interface Release {
  readonly tag: string
  readonly page: string
  readonly assets: readonly { readonly name: string, readonly url: string }[]
}

const SECURITY_HEADERS = {
  'content-security-policy': "default-src 'none'; script-src 'self'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'",
  'referrer-policy': 'no-referrer',
  'x-content-type-options': 'nosniff',
  'x-robots-tag': 'noindex',
}

export default {
  async fetch(request: Request): Promise<Response> {
    if (request.method !== 'GET' && request.method !== 'HEAD') return new Response('method not allowed', { status: 405 })
    const url = new URL(request.url)
    if (url.pathname === '/') return Response.redirect(`${url.origin}/connect`, 302)
    if (url.pathname === '/connect.js') {
      return new Response(SCRIPT, {
        headers: { ...SECURITY_HEADERS, 'content-type': 'text/javascript; charset=utf-8', 'cache-control': 'public, max-age=300' },
      })
    }
    if (url.pathname !== '/connect' && url.pathname !== '/connect/') return new Response('not found', { status: 404 })
    return new Response(pageHtml(await latestRelease()), {
      headers: { ...SECURITY_HEADERS, 'content-type': 'text/html; charset=utf-8', 'cache-control': 'public, max-age=300' },
    })
  },
}

// Releases are prereleases too, so /releases/latest would skip them; the
// newest published entry of the list is the one to offer.
async function latestRelease(): Promise<Release | null> {
  try {
    const response = await fetch(`https://api.github.com/repos/${REPO}/releases?per_page=10`, {
      headers: { accept: 'application/vnd.github+json', 'user-agent': 'prism-connect' },
      cf: { cacheTtl: 300, cacheEverything: true },
    } as RequestInit)
    if (!response.ok) return null
    const list = await response.json()
    if (!Array.isArray(list)) return null
    for (const item of list) {
      if (typeof item !== 'object' || item === null || item.draft === true) continue
      if (typeof item.tag_name !== 'string' || typeof item.html_url !== 'string' || !Array.isArray(item.assets)) continue
      const assets = item.assets.flatMap((asset: unknown) => {
        if (typeof asset !== 'object' || asset === null || !('name' in asset) || !('browser_download_url' in asset)) return []
        const { name, browser_download_url: url } = asset
        return typeof name === 'string' && typeof url === 'string' ? [{ name, url }] : []
      })
      return { tag: item.tag_name, page: item.html_url, assets }
    }
  } catch {
    // The page still works without downloads: it links the releases page.
  }
  return null
}

function downloadsHtml(release: Release | null): string {
  const buttons = DOWNLOADS.flatMap((download) => {
    const asset = release?.assets.find((candidate) => download.pattern.test(candidate.name))
    if (asset === undefined) return []
    return [`<a class="dl" data-os="${download.os}" href="${escapeAttr(asset.url)}">${escapeHtml(download.label)}</a>`]
  })
  const page = release?.page ?? RELEASES_PAGE
  const version = release === null ? '' : ` <span class="tag">${escapeHtml(release.tag)}</span>`
  return `<div class="dls">${buttons.join('')}</div>
<p class="muted">All files / Все файлы:${version} <a href="${escapeAttr(page)}">GitHub</a></p>`
}

function pageHtml(release: Release | null): string {
  return `<!doctype html>
<html lang="ru">
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark">
<title>Prism · connect</title>
<style>
  :root { color-scheme: light dark; --fg: #1d1d1f; --muted: #6e6e73; --line: #d2d2d7; --surface: #f5f5f7; --accent: #0071e3; }
  @media (prefers-color-scheme: dark) { :root { --fg: #f5f5f7; --muted: #a1a1a6; --line: #3a3a3c; --surface: #1c1c1e; --accent: #2997ff; } }
  * { box-sizing: border-box; }
  body { margin: 0; font: 15px/1.5 -apple-system, BlinkMacSystemFont, 'SF Pro Text', system-ui, sans-serif; color: var(--fg); }
  main { width: min(100% - 32px, 560px); margin: 48px auto; }
  h1 { font-size: 26px; margin: 0 0 6px; }
  h2 { font-size: 15px; margin: 28px 0 10px; }
  p { margin: 6px 0; }
  .muted { color: var(--muted); }
  .card { margin-top: 20px; padding: 14px 16px; border: 1px solid var(--line); border-radius: 12px; background: var(--surface); }
  .card code { font-family: ui-monospace, 'SF Mono', SFMono-Regular, Menlo, monospace; font-size: 13px; overflow-wrap: anywhere; }
  .row { display: flex; flex-wrap: wrap; gap: 10px; margin-top: 14px; }
  .btn, .dl { display: inline-block; padding: 9px 16px; border-radius: 9px; border: 1px solid var(--line); color: var(--fg); background: transparent; font: inherit; text-decoration: none; cursor: pointer; }
  .btn--primary, .dl--here { background: var(--accent); border-color: var(--accent); color: #fff; }
  .dls { display: flex; flex-wrap: wrap; gap: 10px; }
  .tag { font-family: ui-monospace, 'SF Mono', SFMono-Regular, Menlo, monospace; }
  a { color: var(--accent); }
  [hidden] { display: none !important; }
</style>
<main>
  <h1>Prism</h1>
  <p>Ссылка добавляет провайдера в Prism. Если Prism установлен, он откроется сам и покажет, что будет добавлено.</p>
  <p class="muted">This link adds a provider to Prism. If Prism is installed, it opens and shows what will be added.</p>

  <div class="card" id="link" hidden>
    <p>Провайдер / Provider: <code id="provider"></code></p>
    <p>Адрес / Endpoint: <code id="endpoint"></code></p>
    <div class="row">
      <a class="btn btn--primary" id="open" href="#">Открыть в Prism / Open in Prism</a>
      <button class="btn" id="copy" type="button">Скопировать ссылку / Copy link</button>
    </div>
    <p class="muted" id="copied" hidden>Скопировано. Вставьте в Prism: Providers → Add from link. / Copied. Paste it in Prism: Providers → Add from link.</p>
  </div>
  <div class="card" id="empty" hidden>
    <p>В ссылке нет данных провайдера: откройте полную ссылку, которую вам прислали.</p>
    <p class="muted">The link has no provider data: open the full link you were sent.</p>
  </div>

  <h2>Нет Prism? Скачайте / No Prism? Download</h2>
  ${downloadsHtml(release)}
  <p class="muted">После установки снова откройте эту ссылку. / After installing, open this link again.</p>
</main>
<script src="/connect.js"></script>
</html>`
}

const SCRIPT = `(() => {
  const params = location.hash.slice(1)
  const fields = new URLSearchParams(params)
  const show = (id) => { document.getElementById(id).hidden = false }
  const os = /Mac/.test(navigator.platform) ? 'mac' : /Win/.test(navigator.platform) ? 'windows' : /Linux/.test(navigator.platform) ? 'linux' : ''
  for (const link of document.querySelectorAll('.dl')) {
    if (link.dataset.os === os) link.classList.add('dl--here')
  }
  if (params === '' || !fields.has('id')) {
    show('empty')
    return
  }
  const app = 'prism://connect?' + params
  let host = ''
  try { host = new URL(fields.get('endpoint') || '').host } catch {}
  document.getElementById('provider').textContent = fields.get('id')
  document.getElementById('endpoint').textContent = host
  document.getElementById('open').href = app
  document.getElementById('copy').addEventListener('click', async () => {
    try {
      await navigator.clipboard.writeText(app)
    } catch {
      const area = document.createElement('textarea')
      area.value = app
      document.body.append(area)
      area.select()
      document.execCommand('copy')
      area.remove()
    }
    show('copied')
  })
  show('link')
  // After load, so the page has rendered before the browser asks to open Prism.
  addEventListener('load', () => setTimeout(() => { location.href = app }, 300))
})()
`

function escapeHtml(value: string): string {
  return value.replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;')
}

function escapeAttr(value: string): string {
  return escapeHtml(value).replaceAll('"', '&quot;')
}
