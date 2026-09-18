import { readFileSync, writeFileSync, existsSync, mkdirSync } from 'node:fs'
import { execFileSync } from 'node:child_process'
import path from 'node:path'

const [dir] = process.argv.slice(2)
if (!dir) {
  console.error('usage: assert-matrix-run.mjs <run-dir>')
  process.exit(2)
}

const runPath = path.join(dir, 'run.json')
if (!existsSync(runPath)) {
  console.error(`FAIL: no run.json in ${dir}`)
  process.exit(1)
}
const run = JSON.parse(readFileSync(runPath, 'utf8'))
const providersPath = path.join(dir, 'daemon/providers.json')
const disabledProviders = new Set(
  existsSync(providersPath)
    ? (JSON.parse(readFileSync(providersPath, 'utf8')).providers ?? [])
        .filter((p) => p.enabled === false)
        .map((p) => p.id)
    : [],
)

const problems = []
function fail(message) {
  problems.push(message)
}

function sha256(file) {
  const tools = [
    ['shasum', ['-a', '256']],
    ['sha256sum', []],
    ['openssl', ['dgst', '-sha256', '-r']],
  ]
  for (const [command, args] of tools) {
    try {
      return execFileSync(command, [...args, file], { encoding: 'utf8' }).split(/\s+/)[0]
    } catch {}
  }
  throw new Error('no sha256 tool available (tried shasum, sha256sum, openssl)')
}

const SCHEMA = 'prism-live-matrix-run/1'
if (run.schema !== SCHEMA) fail(`schema is ${run.schema}, want ${SCHEMA}`)
if (!/^\d{8}-\d{6}\.\d+$/.test(run.runId)) fail(`runId malformed: ${run.runId}`)

const STATIC_MODELS = [
  { id: 'codex/gpt-5.6-luna', provider: 'codex' },
  { id: 'antigravity/gemini-3.7-flash', provider: 'antigravity' },
]
const pairIds = new Set(run.pairs.map((p) => p.modelId))
const derived = [...pairIds].filter((id) => !STATIC_MODELS.some((m) => m.id === id))
const KNOWN_MODELS = [
  ...STATIC_MODELS,
  ...derived.map((id) => ({ id, provider: id.split('/')[0] })),
]
const bySlug = new Map(KNOWN_MODELS.map((m) => [m.id.replace(/[^a-z0-9]/gi, ''), m]))

function grokSelectorToModelId(alias) {
  const key = alias.replace(/^prism-/, '').replace(/-/g, '').toLowerCase()
  const match = bySlug.get(key)
  return match?.id ?? null
}

const EXPECTED_CLIENTS = ['grok', 'omp']

// The requested model lists may carry any non-empty subset of KNOWN_MODELS
// (PRISM_MATRIX_GROK_MODELS/PRISM_MATRIX_OMP_MODELS); every requested entry
// must be known and covered by a pair, and no extra pairs may exist.
const requestedModels = (client) => (client === 'grok' ? run.grokModels ?? [] : run.ompModels ?? [])
for (const client of EXPECTED_CLIENTS) {
  const requested = requestedModels(client)
  if (requested.length === 0) {
    fail(`${client} model list is empty`)
  }
  for (const requestedEntry of requested) {
    const normalized = requestedEntry.replace(/^prism[/-]/, '').replace(/[^a-zA-Z0-9]/g, '').toLowerCase()
    if (!KNOWN_MODELS.some((m) => normalized === m.id.replace(/[^a-z0-9]/gi, '').toLowerCase())) {
      fail(`${client}: unknown requested model ${requestedEntry}`)
    }
  }
}

const byKey = new Map(run.pairs.map((p) => [`${p.client}--${p.modelId}`, p]))
for (const client of EXPECTED_CLIENTS) {
  const requested = requestedModels(client)
  for (const requestedEntry of requested) {
    const modelId = client === 'grok' ? grokSelectorToModelId(requestedEntry) : requestedEntry.replace(/^prism\//, '')
    const model = KNOWN_MODELS.find((m) => m.id === modelId)
    if (model === undefined) {
      fail(`${client}: unknown requested model ${requestedEntry}`)
      continue
    }
    const pair = byKey.get(`${client}--${model.id}`)
    if (disabledProviders.has(model.provider)) {
      if (pair !== undefined) fail(`pair ${client}--${model.id} exists but provider ${model.provider} is disabled`)
      continue
    }
    if (pair === undefined) {
      fail(`missing pair ${client}--${model.id}`)
      continue
    }
    if (pair.client !== client) fail(`pair ${client}--${model.id} records client=${pair.client}`)
    if (pair.modelId !== model.id) fail(`pair records modelId=${pair.modelId}`)
    if (pair.provider !== model.provider) fail(`pair ${client}--${model.id} provider=${pair.provider}, want ${model.provider}`)
    if (pair.exitCode !== 0) fail(`${client}--${model.id} exitCode=${pair.exitCode}`)
    const wantSeconds = Number.isFinite(run.minSeconds) ? run.minSeconds : 300
    if (pair.elapsedSeconds === undefined || pair.elapsedSeconds < wantSeconds) {
      fail(`${client}--${model.id} elapsed=${pair.elapsedSeconds}s, want >= ${wantSeconds}`)
    }
    if (pair.transportErrorCount !== 0) {
      fail(`${client}--${model.id} transportErrorCount=${pair.transportErrorCount}, want 0`)
    }
    if (pair.markerCount !== 1) fail(`${client}--${model.id} markerCount=${pair.markerCount}, want exactly 1`)
    if (pair.finalAssistantChars === undefined || pair.finalAssistantChars < 1000) {
      fail(`${client}--${model.id} finalAssistantChars=${pair.finalAssistantChars}, want >= 1000`)
    }
    if (pair.verdict !== 'pass') fail(`${client}--${model.id} verdict=${pair.verdict}`)
    if (typeof pair.account !== 'string' || pair.account === '') {
      fail(`${client}--${model.id} account not recorded`)
    }
    for (const artifact of [pair.stdoutPath, pair.stderrPath, pair.daemonLogPath]) {
      if (typeof artifact !== 'string' || artifact === '') {
        fail(`pair ${client}--${model.id} artifact path not recorded`)
        continue
      }
      const artifactPath = path.join(dir, artifact)
      if (!existsSync(artifactPath)) fail(`pair ${client}--${model.id} missing artifact ${artifact}`)
    }
  }
}
const expectedPairCount = EXPECTED_CLIENTS.reduce((sum, client) => {
  return sum + requestedModels(client).filter((entry) => {
    const modelId = client === 'grok' ? grokSelectorToModelId(entry) : entry.replace(/^prism\//, '')
    const model = KNOWN_MODELS.find((m) => m.id === modelId)
    return model !== undefined && !disabledProviders.has(model.provider)
  }).length
}, 0)
if (run.pairs.length !== expectedPairCount) {
  fail(`run has ${run.pairs.length} pairs, want ${expectedPairCount}`)
}

for (const required of ['daemon/health.json', 'daemon/usage-before.json', 'daemon/usage-after.json']) {
  if (!existsSync(path.join(dir, required))) fail(`missing ${required}`)
}

const manifestPath = path.join(dir, 'manifest.sha256')
if (!existsSync(manifestPath)) {
  fail('missing manifest.sha256')
} else {
  const manifest = readFileSync(manifestPath, 'utf8')
    .split('\n')
    .filter((line) => line.trim() !== '')
  for (const line of manifest) {
    const [expectedHash, ...rest] = line.trim().split(/\s+/)
    const rel = rest.join(' ').replace(/^\*/, '')
    const abs = path.join(dir, rel)
    if (!existsSync(abs)) {
      fail(`manifest names missing file ${rel}`)
      continue
    }
    const actual = sha256(abs)
    if (actual !== expectedHash) fail(`checksum mismatch for ${rel}`)
  }
}

if (problems.length > 0) {
  console.error(`FAIL: ${problems.length} problem(s):`)
  for (const problem of problems) console.error(`  - ${problem}`)
  process.exit(1)
}

const summary = {
  ok: true,
  runId: run.runId,
  featureId: run.featureId,
  pairs: run.pairs.map((p) => ({ client: p.client, modelId: p.modelId, elapsedSeconds: p.elapsedSeconds, verdict: p.verdict })),
}
const summaryPath = path.join(dir, 'assertion.json')
writeFileSync(summaryPath, JSON.stringify(summary, null, 2))
console.log(JSON.stringify(summary))
