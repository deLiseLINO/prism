import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { execFileSync } from 'node:child_process'
import { existsSync, lstatSync, mkdirSync, readFileSync, readdirSync, writeFileSync } from 'node:fs'
import { basename, dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const scripts = dirname(fileURLToPath(import.meta.url))
const [mode, rootArg, portArg, wsUrl] = process.argv.slice(2)
assert.ok(['seed', 'drive'].includes(mode) && rootArg && /^\d+$/.test(portArg ?? ''),
  'usage: integration-recovery-drive.mjs <seed|drive> <sandbox> <port> [ws-url]')
const root = resolve(rootArg)
assert.equal(lstatSync(root).isSymbolicLink(), false, 'sandbox must be physical')
const evidence = join(root, 'evidence')
const port = Number(portArg)
assert.ok(port > 0 && port < 65536, 'invalid daemon port')
const baseURL = `http://127.0.0.1:${port}`
const configs = {
  codex: '.codex/config.toml',
  grok: '.grok/config.toml',
  omp: '.omp/agent/models.yml',
  claude: '.claude/settings.json',
  pi: '.pi/agent/models.json',
  opencode: '.config/opencode/opencode.json',
  hermes: '.hermes/config.yaml',
}
const ids = Object.keys(configs)
const sideFiles = {
  codex: ['.codex/prism-catalog.json'],
  claude: ['.claude/cache/gateway-models.json'],
}
const legacyFiles = ['.claude/.prism-claude-env.json', '.claude/cache/.prism-gateway-models.json']
const target = (id) => join(root, configs[id])
const journal = (id) => join(dirname(target(id)), `.${basename(target(id))}.prism-journal.json`)
const save = (name, value) => writeFileSync(join(evidence, name), JSON.stringify(value, null, 2) + '\n')
const read = (path) => readFileSync(path, 'utf8')
const hash = (bytes) => createHash('sha256').update(bytes).digest('hex')

// These files are fixture input, not a claim that client binaries are installed.
async function seed() {
  for (const path of Object.values(configs)) mkdirSync(dirname(join(root, path)), { recursive: true })
  mkdirSync(join(root, '.claude/cache'), { recursive: true })
  const legacySettings = {
    env: {
      ANTHROPIC_BASE_URL: baseURL,
      ANTHROPIC_MODEL: 'claude-router--model',
      ANTHROPIC_AUTH_TOKEN: 'prism-loopback',
      USER_SETTING: 'keep-current-value',
    },
    permissions: { allow: ['Read'] },
  }
  writeFileSync(target('claude'), JSON.stringify(legacySettings, null, 2) + '\n')
  writeFileSync(join(root, legacyFiles[0]), '{}\n')
  writeFileSync(join(root, legacyFiles[1]), '{"original":"retained-cache-metadata"}\n')
  writeFileSync(join(root, '.claude/cache/gateway-models.json'),
    JSON.stringify({ baseUrl: baseURL, fetchedAt: 1, models: [{ id: 'claude-router--model', display_name: 'router/model' }] }) + '\n')
  writeFileSync(join(root, '.prism/prism.json'), JSON.stringify({
    version: 1, generation: 0,
    config: {
      version: 1, daemon: { listen: `127.0.0.1:${port}` },
      providers: { router: { wire: 'chat', baseURL: 'https://edge.example.invalid/v1', models: ['model'], enabled: true } },
      combos: {}, routes: {}, aliases: {},
    },
  }, null, 2) + '\n')
  const fonts = [
    ['inter-400.woff2', process.env.PRISM_VERIFY_UI_FONT, 'inter', '400'],
    ['inter-600.woff2', process.env.PRISM_VERIFY_UI_BOLD_FONT ?? process.env.PRISM_VERIFY_UI_FONT, 'inter', '600'],
    ['mono-400.woff2', process.env.PRISM_VERIFY_MONO_FONT, 'jetbrains-mono', '400'],
  ]
  mkdirSync(join(root, 'screenshot-fonts'))
  for (const [name, local, family, weight] of fonts) {
    let bytes
    if (local) bytes = readFileSync(local)
    else {
      const url = `https://cdn.jsdelivr.net/npm/@fontsource/${family}/files/${family}-latin-${weight}-normal.woff2`
      const response = await fetch(url, { signal: AbortSignal.timeout(30_000) })
      assert.ok(response.ok, `screenshot font ${name}: HTTP ${response.status}`)
      bytes = Buffer.from(await response.arrayBuffer())
      assert.ok(['wOF2', 'wOFF'].includes(bytes.toString('ascii', 0, 4)), `invalid font download: ${name}`)
    }
    writeFileSync(join(root, 'screenshot-fonts', name), bytes)
  }
  save('scope.json', {
    checks: ['seven conflict badges before Apply', 'Review, Apply and Rollback confirmation cancellation without filesystem writes',
      'current config, side file and metadata backups', 'healthy refreshed badges', 'exact claude and codex rollback baseline',
      'legacy claude router/model with loopback token and empty env journal'],
    inference: false, clientBinaryExecution: false, fonts: 'screenshot-only local files',
  })
}

function evaluate(code, args = []) {
  const expression = `await (${code.toString()})(...${JSON.stringify(args)})`
  return JSON.parse(execFileSync(process.execPath, [join(scripts, 'cdp-eval.mjs'), wsUrl, expression, '30000'],
    { encoding: 'utf8', timeout: 35_000, maxBuffer: 4 * 1024 * 1024 }))
}

let browserSocket
let commandId = 0
async function command(method, params) {
  if (!browserSocket) {
    browserSocket = new WebSocket(wsUrl)
    await new Promise((ready, reject) => {
      browserSocket.addEventListener('open', ready, { once: true })
      browserSocket.addEventListener('error', reject, { once: true })
    })
  }
  const socket = browserSocket
  const id = ++commandId
  return await new Promise((resolveCommand, reject) => {
    const timer = setTimeout(() => { socket.close(); reject(new Error(`CDP ${method} timed out`)) }, 10_000)
    const listener = (event) => {
      const reply = JSON.parse(event.data.toString())
      if (reply.id !== id) return
      clearTimeout(timer)
      socket.removeEventListener('message', listener)
      if (reply.error) reject(new Error(JSON.stringify(reply.error)))
      else resolveCommand(reply.result)
    }
    socket.addEventListener('message', listener)
    socket.send(JSON.stringify({ id, method, params }))
  })
}

async function prepareScreenshots() {
  await command('Emulation.setDeviceMetricsOverride', { width: 1440, height: 1800, deviceScaleFactor: 2, mobile: false })
  const fonts = ['inter-400.woff2', 'inter-600.woff2', 'mono-400.woff2']
    .map((name) => readFileSync(join(root, 'screenshot-fonts', name)).toString('base64'))
  save('screenshot-settings.json', evaluate(async (sources) => {
    for (const [index, source] of sources.entries()) {
      const bytes = Uint8Array.from(atob(source), (character) => character.charCodeAt(0))
      const font = new FontFace(index === 2 ? 'JetBrains Mono' : 'Inter', bytes.buffer,
        { weight: index === 1 ? '600' : '400' })
      await font.load()
      document.fonts.add(font)
    }
    const style = document.createElement('style')
    style.textContent = ':root { --font-ui: Inter, sans-serif !important; --font-mono: "JetBrains Mono", monospace !important; }'
    document.head.append(style)
    await document.fonts.ready
    if (devicePixelRatio !== 2) throw new Error('screenshots require deviceScaleFactor 2')
    if (!document.fonts.check('400 12px Inter') || !document.fonts.check('400 12px "JetBrains Mono"')) throw new Error('screenshot fonts not loaded')
    return { deviceScaleFactor: devicePixelRatio, width: innerWidth, height: innerHeight, font: getComputedStyle(document.body).fontFamily }
  }, [fonts]))
}

async function screenshot(name, id) {
  evaluate(async (id) => {
    const node = id ? [...document.querySelectorAll('.int-item')].find((row) =>
      row.querySelector('.lrow__title > span:first-child')?.textContent.trim() === id) : document.querySelector('main h1')
    if (!node) throw new Error('screenshot target missing')
    node.scrollIntoView({ block: 'start', behavior: 'instant' })
    await new Promise((done) => requestAnimationFrame(() => requestAnimationFrame(done)))
    return true
  }, [id ?? null])
  const output = join(evidence, name)
  const captured = await command('Page.captureScreenshot', { format: 'png', captureBeyondViewport: false, fromSurface: true })
  writeFileSync(output, Buffer.from(captured.data, 'base64'))
  const png = readFileSync(output)
  assert.equal(png.subarray(1, 4).toString(), 'PNG')
  assert.equal(png.readUInt32BE(16), 2880, 'screenshot width must be doubled')
  assert.equal(png.readUInt32BE(20), 3600, 'screenshot height must be doubled')
}

function navigate(view) {
  return evaluate(async (view) => {
    const button = [...document.querySelectorAll('nav button')].find((node) => node.textContent.trim() === view)
    if (!button) throw new Error(`${view} navigation missing`)
    button.click()
    for (let n = 0; n < 150; n++) {
      if (document.querySelector('main h1')?.textContent.trim() === view &&
        (view !== 'Integrations' || document.querySelectorAll('.int-item').length === 7)) return true
      await new Promise((done) => setTimeout(done, 100))
    }
    throw new Error(`${view} did not render`)
  }, [view])
}

function refresh() {
  navigate('Overview')
  navigate('Integrations')
}

function waitBadge(id, expected) {
  return evaluate(async (id, expected) => {
    for (let n = 0; n < 150; n++) {
      const row = [...document.querySelectorAll('.int-item')].find((node) => node.querySelector('.lrow__title > span:first-child')?.textContent.trim() === id)
      const alert = row?.querySelector('[role=alert]')
      if (alert) throw new Error(`${id}: ${alert.textContent}`)
      const apply = [...(row?.querySelectorAll('button') ?? [])].find((node) => node.textContent.trim() === 'Apply')
      if (row?.querySelector('.lrow__title .chip')?.textContent.trim() === expected && !row.querySelector('[role=alertdialog]') && apply && !apply.disabled) {
        return { id, badge: expected, text: row.innerText }
      }
      await new Promise((done) => setTimeout(done, 100))
    }
    throw new Error(`${id} never reached ${expected}`)
  }, [id, expected])
}

function click(id, label, dialog = false) {
  return evaluate(async (id, label, dialog) => {
    const row = [...document.querySelectorAll('.int-item')].find((node) => node.querySelector('.lrow__title > span:first-child')?.textContent.trim() === id)
    if (!row) throw new Error(`${id} row missing`)
    const scope = dialog ? row.querySelector('[role=alertdialog]') : row
    const button = [...(scope?.querySelectorAll('button') ?? [])].find((node) => node.textContent.trim() === label)
    if (!button || button.disabled) throw new Error(`${id} ${label} absent or disabled`)
    button.scrollIntoView({ block: 'center', behavior: 'instant' })
    button.click()
    await new Promise((done) => requestAnimationFrame(() => requestAnimationFrame(done)))
    return { id, label }
  }, [id, label, dialog])
}

function dialogText(id, action) {
  return evaluate(async (id, action) => {
    for (let n = 0; n < 100; n++) {
      const row = [...document.querySelectorAll('.int-item')].find((node) => node.querySelector('.lrow__title > span:first-child')?.textContent.trim() === id)
      const dialog = row?.querySelector('[role=alertdialog]')
      if (dialog) {
        const text = dialog.innerText
        const title = dialog.querySelector('.confirm__title')?.textContent.trim()
        if (!title?.startsWith(action === 'apply' ? 'Reconnect ' : 'Remove Prism from ')) throw new Error('wrong confirmation: ' + title)
        if (!text.includes('saves the current config') || !text.includes('recovery metadata')) throw new Error('missing preservation warning')
        if (action === 'apply' && (!text.includes('new rollback baseline') || !text.includes('not automatically restorable'))) throw new Error('missing baseline warning')
        return { id, title, text }
      }
      await new Promise((done) => setTimeout(done, 100))
    }
    throw new Error(`${id} confirmation absent`)
  }, [id, action])
}

function snapshot() {
  const state = {}
  function visit(path) {
    if (!existsSync(path)) return
    const stat = lstatSync(path, { bigint: true })
    assert.equal(stat.isSymbolicLink(), false, `fixture symlink: ${path}`)
    const key = path.slice(root.length + 1)
    state[key] = { kind: stat.isDirectory() ? 'directory' : 'file', mode: String(stat.mode), mtimeNs: String(stat.mtimeNs) }
    if (stat.isDirectory()) for (const name of readdirSync(path).sort()) visit(join(path, name))
    else state[key].sha256 = hash(readFileSync(path))
  }
  for (const path of ['.codex', '.grok', '.omp', '.claude', '.pi', '.config/opencode', '.hermes', '.prism/prism.json']) visit(join(root, path))
  return state
}

function noWrites(before, name) {
  const after = snapshot()
  save(`${name}-filesystem.json`, { before, after })
  assert.deepEqual(after, before, `${name} changed config, metadata, backup files or directory entries`)
}

async function inspect(name) {
  const response = await fetch(`${baseURL}/api/v1/integrations`, { signal: AbortSignal.timeout(5000) })
  assert.equal(response.status, 200)
  const status = await response.json()
  save(`${name}-api.json`, status)
  assert.equal(status.integrations.length, 7)
  for (const entry of status.integrations) {
    assert.equal(entry.installed, true, `${entry.id} target directory not detected`)
    assert.equal(entry.enabled, false, `${entry.id} auto-apply must stay off`)
  }
  return status.integrations
}

function checkCancel(id, action, trigger, escape = false) {
  const before = snapshot()
  click(id, trigger)
  save(`${id}-${action}-${trigger.toLowerCase()}-confirmation.json`, dialogText(id, action))
  noWrites(before, `${id}-${action}-${trigger.toLowerCase()}-open`)
  if (escape) evaluate(() => { window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })); return true })
  else click(id, 'Cancel', true)
  waitBadge(id, 'Needs repair')
  noWrites(before, `${id}-${action}-${trigger.toLowerCase()}-cancel`)
}

function beforeReconnect(id) {
  const paths = [target(id), journal(id), ...(sideFiles[id] ?? []).map((path) => join(root, path))]
  if (id === 'claude') paths.push(...legacyFiles.map((path) => join(root, path)))
  return new Map(paths.filter(existsSync).map((path) => [path, readFileSync(path)]))
}

function receipt(id) {
  return evaluate((id) => {
    const row = [...document.querySelectorAll('.int-item')].find((node) => node.querySelector('.lrow__title > span:first-child')?.textContent.trim() === id)
    const note = [...(row?.querySelectorAll('.lrow-note') ?? [])].find((node) => node.querySelector('.lrow-note__title')?.textContent.trim() === 'Applied; backups saved')
    if (!note) throw new Error(`${id} backup receipt absent`)
    for (const summary of note.querySelectorAll('summary')) summary.click()
    const paths = [...note.querySelectorAll('dd')].map((node) => node.textContent.trim())
    if (!paths.length) throw new Error(`${id} receipt paths absent`)
    return paths
  }, [id])
}

async function reconnect(id, before, name) {
  click(id, 'Apply')
  save(`${name}-confirmation.json`, dialogText(id, 'apply'))
  if (id === 'claude') await screenshot(`${name}-confirmation.png`, id)
  for (const [path, bytes] of before) assert.deepEqual(readFileSync(path), bytes, `${id} changed files before confirmation`)
  click(id, 'Reconnect', true)
  save(`${name}-managed.json`, waitBadge(id, 'managed'))
  const paths = receipt(id)
  for (const path of paths) {
    assert.ok(resolve(path).startsWith(root + '/') && !lstatSync(path).isSymbolicLink(), 'unsafe receipt path')
    assert.ok(lstatSync(path).isFile(), 'receipt backup must exist')
  }
  const matches = []
  for (const [original, bytes] of before) {
    const backup = paths.find((path) => path.startsWith(original + '.bak') && readFileSync(path).equals(bytes))
    assert.ok(backup, `${id} did not report an exact backup of ${original}`)
    const retained = join(evidence, 'backups', name, basename(backup))
    mkdirSync(dirname(retained), { recursive: true })
    writeFileSync(retained, readFileSync(backup))
    matches.push({ original, backup, evidenceFile: retained.slice(evidence.length + 1), sha256: hash(bytes) })
  }
  save(`${name}-backups.json`, { displayedPaths: paths, exactMatches: matches })
  const baseline = JSON.parse(read(journal(id)))
  assert.equal(baseline.currentBaseline, true, `${id} reconnect did not publish a new baseline`)
  for (const [path, bytes] of before) {
    if (path !== target(id) && !(sideFiles[id] ?? []).some((relative) => join(root, relative) === path)) continue
    const file = baseline.files.find((file) => file.path === path)
    assert.ok(file?.original.present, `${id} baseline lost ${path}`)
    assert.equal(file.original.text, bytes.toString('utf8'), `${id} baseline does not keep current bytes`)
  }
  save(`${name}-journal.json`, baseline)
  return matches
}

function assertBinding(id) {
  const text = read(target(id))
  if (id === 'claude') {
    const settings = JSON.parse(text)
    assert.equal(settings.env.ANTHROPIC_BASE_URL, baseURL)
    assert.equal(settings.env.ANTHROPIC_MODEL, 'claude-router--model')
    assert.equal(settings.env.ANTHROPIC_AUTH_TOKEN, 'prism-loopback')
    assert.equal(settings.env.USER_SETTING, 'keep-current-value')
    assert.equal(JSON.parse(read(join(root, sideFiles.claude[0]))).baseUrl, baseURL)
  } else {
    assert.ok(text.includes(`${baseURL}/v1`), `${id} config not reconnected to owned daemon`)
    if (id === 'codex') {
      assert.ok(text.includes(`openai_base_url = "${baseURL}/v1"`), 'codex routing not rewritten')
      assert.ok(text.includes(`model_catalog_json = "${join(root, sideFiles.codex[0])}"`), 'codex catalog binding missing')
      assert.ok(JSON.parse(read(join(root, sideFiles.codex[0]))).models.some((model) => model.slug === 'router/model'), 'codex catalog not regenerated')
    }
  }
}

function rollbackExact(id, before, name) {
  click(id, 'Rollback')
  // A baseline can itself contain Prism fields. Exact restoration, not an
  // unmanaged label, is the contract after this normal rollback.
  evaluate(async (id) => {
    for (let n = 0; n < 150; n++) {
      const row = [...document.querySelectorAll('.int-item')].find((node) => node.querySelector('.lrow__title > span:first-child')?.textContent.trim() === id)
      if (row?.querySelector('[role=alert]')) throw new Error(row.innerText)
      if (row?.querySelector('[role=alertdialog]')) throw new Error('healthy Rollback unexpectedly needs confirmation')
      if (row && ![...row.querySelectorAll('button')].some((node) => node.disabled) && !row.innerText.includes('backups saved')) return true
      await new Promise((done) => setTimeout(done, 100))
    }
    throw new Error(`${id} Rollback did not settle`)
  }, [id])
  const files = []
  for (const path of [target(id), ...(sideFiles[id] ?? []).map((path) => join(root, path))]) {
    const expected = before.get(path)
    assert.ok(expected, `missing rollback fixture ${path}`)
    assert.deepEqual(readFileSync(path), expected, `${id} Rollback did not restore the pre-reconnect bytes`)
    const retained = join(evidence, 'rollback', name, basename(path))
    mkdirSync(dirname(retained), { recursive: true })
    writeFileSync(retained, readFileSync(path))
    files.push({ path, evidenceFile: retained.slice(evidence.length + 1), sha256: hash(expected) })
  }
  const state = JSON.parse(read(journal(id)))
  assert.equal(state.retired, true, 'Rollback journal not retired')
  save(`${name}-rollback.json`, { exactFiles: files, retired: state.retired })
}

async function drive() {
  assert.ok(wsUrl?.startsWith('ws://127.0.0.1:'), 'owned renderer WebSocket required')
  await prepareScreenshots()
  navigate('Experimental')
  save('other-agents.json', evaluate(async () => {
    for (let n = 0; n < 100; n++) {
      const label = [...document.querySelectorAll('.experimental-flag')].find((node) => node.textContent.includes('Other agents'))
      const input = label?.querySelector('input[type=checkbox]')
      if (input) {
        if (!input.checked) label.click()
        for (let i = 0; i < 100; i++) {
          if (input.checked) return { otherAgents: true }
          await new Promise((done) => setTimeout(done, 100))
        }
        throw new Error('Other agents did not turn on')
      }
      await new Promise((done) => setTimeout(done, 100))
    }
    throw new Error('Other agents setting absent')
  }))
  navigate('Integrations')
  const legacy = beforeReconnect('claude')
  const beforeLegacyReview = snapshot()
  const settings = JSON.parse(read(target('claude')))
  assert.equal(settings.env.ANTHROPIC_MODEL, 'claude-router--model')
  assert.equal(settings.env.ANTHROPIC_AUTH_TOKEN, 'prism-loopback')
  assert.deepEqual(JSON.parse(read(join(root, legacyFiles[0]))), {})
  save('legacy-claude-input.json', { settings, envJournal: {}, activeJournalPresent: existsSync(journal('claude')) })
  writeFileSync(join(evidence, 'legacy-claude-settings.json'), readFileSync(target('claude')))
  writeFileSync(join(evidence, 'legacy-claude-env-journal.json'), readFileSync(join(root, legacyFiles[0])))
  assert.equal(existsSync(journal('claude')), false, 'legacy scenario must not have a modern journal')
  save('legacy-claude-badge.json', waitBadge('claude', 'Needs repair'))
  await screenshot('legacy-claude-needs-repair.png', 'claude')
  const legacyStatus = await inspect('legacy-before-apply')
  assert.equal(legacyStatus.find((entry) => entry.id === 'claude')?.conflict, 'journal')
  noWrites(beforeLegacyReview, 'legacy-status-inspection')
  checkCancel('claude', 'apply', 'Review')
  const preservedLegacy = await reconnect('claude', legacy, 'legacy-claude')
  assertBinding('claude')
  for (const path of legacyFiles) assert.equal(existsSync(join(root, path)), false, 'legacy metadata must be archived, not re-imported')
  await screenshot('legacy-claude-recovered.png', 'claude')
  rollbackExact('claude', legacy, 'legacy-claude')

  console.log('==> creating all seven configurations through rendered Apply')
  for (const id of ids) {
    click(id, 'Apply')
    save(`${id}-initial-apply.json`, waitBadge(id, 'managed'))
    assertBinding(id)
    assert.ok(existsSync(journal(id)), `${id} Apply did not create recovery metadata`)
  }
  await inspect('all-managed')

  console.log('==> corrupting only sandbox recovery metadata and representative current config')
  const current = JSON.parse(read(target('claude')))
  current.env.ANTHROPIC_MODEL = 'current-user-model'
  current.env.ANTHROPIC_AUTH_TOKEN = 'current-user-token'
  writeFileSync(target('claude'), JSON.stringify(current, null, 2) + '\n')
  const cache = JSON.parse(read(join(root, sideFiles.claude[0])))
  cache.models.push({ id: 'claude-router--current', display_name: 'current baseline' })
  writeFileSync(join(root, sideFiles.claude[0]), JSON.stringify(cache, null, 2) + '\n')
  const codex = read(target('codex'))
  assert.ok(codex.includes(`openai_base_url = "${baseURL}/v1"`))
  writeFileSync(target('codex'), codex.replace(`openai_base_url = "${baseURL}/v1"`, 'openai_base_url = "https://edge.example.invalid/v1"') + '# User baseline must survive rollback.\n')
  writeFileSync(join(root, sideFiles.codex[0]), '{"models":[],"userBaseline":"keep-catalog-bytes"}\n')
  for (const id of ids) {
    const metadata = JSON.parse(read(journal(id)))
    metadata.version = 999
    writeFileSync(journal(id), JSON.stringify(metadata, null, 2) + '\n')
  }
  const baselines = new Map(ids.map((id) => [id, beforeReconnect(id)]))
  const beforeStatusRefresh = snapshot()
  refresh()
  for (const id of ids) save(`${id}-before-apply-badge.json`, waitBadge(id, 'Needs repair'))
  const conflicts = await inspect('all-conflict-before-apply')
  for (const status of conflicts) assert.equal(status.conflict, 'journal', `${status.id} status omitted conflict`)
  save('conflict-toolbar.json', evaluate(() => {
    const buttons = [...document.querySelectorAll('.toolbar button')]
    for (const label of ['Apply all', 'Rollback all']) {
      if (!buttons.find((node) => node.textContent.trim() === label)?.disabled) throw new Error(`${label} must exclude conflict cards`)
    }
    return { applyAllDisabled: true, rollbackAllDisabled: true, cards: [...document.querySelectorAll('.int-item')].map((row) => row.innerText) }
  }))
  await screenshot('conflict-overview.png')
  noWrites(beforeStatusRefresh, 'conflict-status-refresh')

  console.log('==> Review and cancelled actions leave every config and metadata file untouched')
  for (const id of ids) {
    checkCancel(id, 'apply', 'Review')
    checkCancel(id, 'apply', 'Apply', true)
    checkCancel(id, 'rollback', 'Rollback')
  }
  const recovered = []
  console.log('==> confirming reconnect and reading every displayed backup')
  for (const id of ids) {
    const matches = await reconnect(id, baselines.get(id), `${id}-reconnect`)
    assertBinding(id)
    recovered.push({ id, exactBackups: matches })
  }
  refresh()
  for (const id of ids) save(`${id}-refreshed-healthy.json`, waitBadge(id, 'managed'))
  const healthy = await inspect('all-recovered')
  for (const status of healthy) {
    assert.ok(status.conflict === undefined || status.conflict === '', `${status.id} conflict remained`)
    assert.equal(status.managed, true)
    assert.equal(status.drift, false)
    assert.ok(status.blocked === undefined || status.blocked === false, `${status.id} became blocked`)
  }
  await screenshot('recovered-overview.png')

  console.log('==> normal Rollback restores current claude and codex config and side files exactly')
  for (const id of ['claude', 'codex']) rollbackExact(id, baselines.get(id), id)
  for (const { backup, sha256 } of [...preservedLegacy, ...recovered.flatMap((entry) => entry.exactBackups)]) {
    assert.equal(hash(readFileSync(backup)), sha256, 'a later action overwrote an earlier backup')
  }
  await inspect('after-representative-rollback')
  save('recovery-summary.json', {
    ok: true, ids, conflictBadgesBeforeApply: 7, reviewCancelNoWrites: 7,
    applyEscapeNoWrites: 7, rollbackCancelNoWrites: 7, reconnectedAndRefreshed: 7,
    exactRollbackClients: ['claude', 'codex'], legacyClaude: true,
    inference: false, clientBinaryExecution: false,
    screenshots: ['legacy-claude-needs-repair.png', 'legacy-claude-confirmation.png', 'legacy-claude-recovered.png',
      'conflict-overview.png', 'claude-reconnect-confirmation.png', 'recovered-overview.png'],
  })
}

try {
  if (mode === 'seed') await seed()
  else await drive()
} catch (error) {
  if (mode === 'drive') {
    try { save('failure-renderer.json', evaluate(() => ({ hash: location.hash, text: document.querySelector('main')?.innerText }))) } catch { /* Keep the original failure. */ }
    try { await screenshot('failure-renderer.png') } catch { /* Teardown still owns process cleanup. */ }
  }
  save('failure.json', { message: error.message, stack: error.stack })
  console.error(error.stack)
  process.exitCode = 1
} finally {
  browserSocket?.close()
}
