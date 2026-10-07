import { createHash } from 'node:crypto'
import { lstatSync, readFileSync, readdirSync, unlinkSync, writeFileSync } from 'node:fs'
import path from 'node:path'
import { parseNdjson, parseOmpEntries } from './omp-transcript.mjs'

const [dir] = process.argv.slice(2)
if (!dir) {
  console.error('usage: assert-matrix-run.mjs <run-dir>')
  process.exit(2)
}

const problems = []
const fail = (message) => problems.push(message)
function finish() {
  console.error(`FAIL: ${problems.length} problem(s):`)
  for (const problem of problems) console.error(`  - ${problem}`)
  process.exit(1)
}

let rootStat = null
try { rootStat = lstatSync(dir) } catch {}
if (rootStat === null || rootStat.isSymbolicLink() || !rootStat.isDirectory()) {
  console.error(rootStat === null ? `FAIL: no run.json in ${dir}` : `FAIL: run directory ${dir} is not a plain directory`)
  process.exit(1)
}

const assertionPath = path.join(dir, 'assertion.json')
try {
  const existing = lstatSync(assertionPath)
  if (existing.isDirectory()) fail('assertion.json is a directory')
  else {
    if (existing.isSymbolicLink()) fail('assertion.json is a symlink')
    unlinkSync(assertionPath)
  }
} catch (error) {
  if (error.code !== 'ENOENT') fail(`assertion.json cannot be cleared: ${error.message}`)
}

const EXCLUDED_ROOT = new Set(['manifest.sha256', 'assertion.json'])
const inventory = new Map()
function walk(rel) {
  for (const entry of readdirSync(path.join(dir, rel), { withFileTypes: true })) {
    const next = rel === '' ? entry.name : `${rel}/${entry.name}`
    const stat = lstatSync(path.join(dir, next))
    if (rel === '' && EXCLUDED_ROOT.has(entry.name)) {
      if (!stat.isFile() && entry.name === 'manifest.sha256') fail('manifest.sha256 is not a regular file')
      continue
    }
    if (stat.isSymbolicLink()) fail(`symlink in run evidence: ${next}`)
    else if (stat.isDirectory()) walk(next)
    else if (stat.isFile()) inventory.set(next, readFileSync(path.join(dir, next)))
    else fail(`special file in run evidence (not a regular file): ${next}`)
  }
}
walk('')

function canonicalPath(value) {
  if (typeof value !== 'string' || value === '' || value.includes('\0') || value.includes('\\') || value.includes('\n')) throw new Error('unsafe path')
  if (value.startsWith('/')) throw new Error('unsafe path (absolute)')
  const rel = value.startsWith('./') ? value.slice(2) : value
  if (rel.split('/').some((part) => part === '' || part === '.' || part === '..')) throw new Error('unsafe path')
  return rel
}
const sha256 = (bytes) => createHash('sha256').update(bytes).digest('hex')

let run
const runBytes = inventory.get('run.json')
if (runBytes === undefined) { fail('no run.json in run directory'); finish() }
try { run = JSON.parse(runBytes.toString('utf8')) } catch { fail('run.json is not valid JSON'); finish() }
if (run === null || typeof run !== 'object' || !Array.isArray(run.pairs) || run.pairs.some((pair) => pair === null || typeof pair !== 'object' || Array.isArray(pair))) {
  fail('run.json has no valid pairs array')
  finish()
}
let disabledProviders = new Set()
if (inventory.has('daemon/providers.json')) {
  try {
    disabledProviders = new Set((JSON.parse(inventory.get('daemon/providers.json').toString('utf8')).providers ?? [])
      .filter((p) => p.enabled === false).map((p) => p.id))
  } catch { fail('daemon/providers.json is not valid JSON') }
}

const codePoints = (value) => [...value].length
const isCount = (value) => Number.isSafeInteger(value) && value >= 0

function inspectGrokRequests(source, label) {
  const requests = []
  let open = null
  for (const { line, record } of parseNdjson(source, label)) {
    open ??= { segments: [], pending: '', chars: 0 }
    switch (record.type) {
      case 'text':
        if (typeof record.data !== 'string') throw new Error(`${label} text record at line ${line} has no string data`)
        open.pending += record.data
        open.chars += codePoints(record.data)
        break
      case 'usage':
        open.segments.push(open.pending)
        open.pending = ''
        break
      case 'error':
        throw new Error(`${label} error record at line ${line}`)
      case 'tool_call':
      case 'tool_call_update':
        if (record.status === 'failed') throw new Error(`${label} failed tool call at line ${line}`)
        break
      case 'end': {
        if (open.pending !== '') open.segments.push(open.pending)
        if (record.stopReason !== 'end_turn') throw new Error(`${label} end at line ${line} has stopReason ${record.stopReason}, want end_turn`)
        const finalText = open.segments.at(-1) ?? ''
        if (finalText.trim() === '') throw new Error(`${label} request ending at line ${line} has no final response text`)
        requests.push({ finalText, observedTextChars: open.chars })
        open = null
        break
      }
      default:
    }
  }
  if (open !== null) throw new Error(`${label} transcript ends with an incomplete request (no end record)`)
  return requests
}

function inspectOmpRequests(source, label) {
  const requests = []
  let open = null
  for (const entry of parseNdjson(source, label)) {
    const type = entry.record.type
    if (type === 'agent_start') {
      if (open !== null) throw new Error(`${label} agent_start at line ${entry.line} nested in an open invocation`)
      open = [entry]
      continue
    }
    if (open === null) {
      if (type === 'session') continue
      throw new Error(`${label} record outside agent lifecycle at line ${entry.line}`)
    }
    open.push(entry)
    if (type !== 'agent_end') continue
    const facts = parseOmpEntries(open)
    if (facts.errors.length > 0) throw new Error(`${label} invocation error at line ${facts.errors[0].line}: ${facts.errors[0].reason}`)
    if (facts.final === null) throw new Error(`${label} invocation ending at line ${entry.line} has no completed final assistant text`)
    requests.push({ finalText: facts.final.text, observedTextChars: codePoints(facts.final.text) })
    open = null
  }
  if (open !== null) throw new Error(`${label} transcript ends with an incomplete invocation (no agent_end)`)
  return requests
}

const MARKER_PATTERNS = { grok: /PRISM_MATRIX_GROK_[A-Za-z0-9]+/g, omp: /PRISM_MATRIX_OMP_[A-Za-z0-9]+/g }
function artifactBytes(label, value) {
  if (typeof value !== 'string' || value === '') { fail(`pair ${label} artifact path not recorded`); return null }
  let rel
  try { rel = canonicalPath(value) } catch (error) { fail(`pair ${label} artifact ${error.message}: ${value}`); return null }
  const bytes = inventory.get(rel)
  if (bytes === undefined) fail(`pair ${label} missing artifact ${value}`)
  return bytes ?? null
}
function checkPair(label, client, pair) {
  for (const field of ['failedAttemptCount', 'failedAttemptErrorCount']) {
    if (!isCount(pair[field])) fail(`${label} ${field}=${JSON.stringify(pair[field])}, want integer 0`)
    else if (pair[field] !== 0) fail(`${label} ${field}=${pair[field]}, want 0`)
  }
  for (const field of ['attemptCount', 'requestCount']) {
    if (!Number.isSafeInteger(pair[field]) || pair[field] < 1) fail(`${label} ${field}=${JSON.stringify(pair[field])}, want positive integer`)
  }
  const stdout = artifactBytes(label, pair.stdoutPath)
  artifactBytes(label, pair.stderrPath)
  artifactBytes(label, pair.daemonLogPath)
  if (typeof pair.stdoutPath === 'string') {
    const failedDir = `${pair.stdoutPath.replace(/^\.\//, '').replace(/\.(stdout|ndjson)$/, '')}-failed/`
    const retained = [...inventory.keys()].filter((rel) => rel.startsWith(failedDir))
    if (retained.length > 0) fail(`${label} retains failed-attempt artifacts (${retained[0]}); a passing pair has none`)
  }
  if (stdout === null) return
  let requests
  try {
    const source = stdout.toString('utf8')
    requests = client === 'grok' ? inspectGrokRequests(source, label) : inspectOmpRequests(source, label)
  } catch (error) { fail(error.message); return }
  if (requests.length === 0) { fail(`${label} transcript has no completed request`); return }
  if (pair.requestCount !== requests.length) fail(`${label} requestCount=${pair.requestCount}, raw completed requests=${requests.length}`)
  if (pair.attemptCount !== requests.length) fail(`${label} attemptCount=${pair.attemptCount}, want ${requests.length} (requestCount with no failed attempts)`)
  const markers = requests.reduce((sum, request) => sum + (request.finalText.match(MARKER_PATTERNS[client])?.length ?? 0), 0)
  if (markers !== 1) fail(`${label} raw transcript has ${markers} ${client} marker(s) in completed final text, want exactly 1`)
  if (pair.markerCount !== markers) fail(`${label} markerCount=${pair.markerCount}, raw=${markers}`)
  const chars = requests.reduce((sum, request) => sum + request.observedTextChars, 0)
  if (chars < 1000) fail(`${label} raw assistant chars=${chars}, want >= 1000`)
  if (pair.finalAssistantChars !== chars) fail(`${label} finalAssistantChars=${pair.finalAssistantChars}, raw=${chars}`)
}

const SCHEMA = 'prism-live-matrix-run/1'
if (run.schema !== SCHEMA) fail(`schema is ${run.schema}, want ${SCHEMA}`)
if (!/^\d{8}-\d{6}\.\d+$/.test(run.runId)) fail(`runId malformed: ${run.runId}`)
if (!Number.isFinite(run.minSeconds) || run.minSeconds < 0) fail('minSeconds must be a non-negative number')
const STATIC_MODELS = [
  { id: 'codex/gpt-5.6-luna', provider: 'codex' },
  { id: 'antigravity/gemini-3.7-flash', provider: 'antigravity' },
]
if (run.pairs.some((pair) => typeof pair.modelId !== 'string' || pair.modelId === '')) { fail('pair has no modelId'); finish() }
const pairIds = new Set(run.pairs.map((p) => p.modelId))
const derived = [...pairIds].filter((id) => !STATIC_MODELS.some((m) => m.id === id))
const KNOWN_MODELS = [...STATIC_MODELS, ...derived.map((id) => ({ id, provider: id.split('/')[0] }))]
const bySlug = new Map(KNOWN_MODELS.map((m) => [m.id.replace(/[^a-z0-9]/gi, ''), m]))
function grokSelectorToModelId(alias) {
  const key = alias.replace(/^prism-/, '').replace(/-/g, '').toLowerCase()
  return bySlug.get(key)?.id ?? null
}
const EXPECTED_CLIENTS = ['grok', 'omp']
const requestedModels = (client) => (client === 'grok' ? run.grokModels ?? [] : run.ompModels ?? [])
for (const client of EXPECTED_CLIENTS) {
  const requested = requestedModels(client)
  if (!Array.isArray(requested) || requested.some((entry) => typeof entry !== 'string')) { fail(`${client} model list is invalid`); finish() }
  if (requested.length === 0) fail(`${client} model list is empty`)
  if (new Set(requested).size !== requested.length) fail(`${client} model list contains duplicates`)
  for (const requestedEntry of requested) {
    const normalized = requestedEntry.replace(/^prism[/-]/, '').replace(/[^a-zA-Z0-9]/g, '').toLowerCase()
    if (!KNOWN_MODELS.some((m) => normalized === m.id.replace(/[^a-z0-9]/gi, '').toLowerCase())) fail(`${client}: unknown requested model ${requestedEntry}`)
  }
}
const byKey = new Map(run.pairs.map((p) => [`${p.client}--${p.modelId}`, p]))
if (byKey.size !== run.pairs.length) fail('duplicate pair in run.json')
for (const client of EXPECTED_CLIENTS) {
  for (const requestedEntry of requestedModels(client)) {
    const modelId = client === 'grok' ? grokSelectorToModelId(requestedEntry) : requestedEntry.replace(/^prism\//, '')
    const model = KNOWN_MODELS.find((m) => m.id === modelId)
    if (model === undefined) { fail(`${client}: unknown requested model ${requestedEntry}`); continue }
    const pair = byKey.get(`${client}--${model.id}`)
    if (disabledProviders.has(model.provider)) {
      if (pair !== undefined) fail(`pair ${client}--${model.id} exists but provider ${model.provider} is disabled`)
      continue
    }
    if (pair === undefined) { fail(`missing pair ${client}--${model.id}`); continue }
    if (pair.provider !== model.provider) fail(`pair ${client}--${model.id} provider=${pair.provider}, want ${model.provider}`)
    if (pair.exitCode !== 0) fail(`${client}--${model.id} exitCode=${pair.exitCode}`)
    if (!Number.isFinite(pair.elapsedSeconds) || pair.elapsedSeconds < run.minSeconds) fail(`${client}--${model.id} elapsed=${pair.elapsedSeconds}s, want >= ${run.minSeconds}`)
    if (pair.transportErrorCount !== 0) fail(`${client}--${model.id} transportErrorCount=${pair.transportErrorCount}, want 0`)
    if (pair.verdict !== 'pass') fail(`${client}--${model.id} verdict=${pair.verdict}`)
    if (typeof pair.account !== 'string' || pair.account === '') fail(`${client}--${model.id} account not recorded`)
    checkPair(`${client}--${model.id}`, client, pair)
  }
}
const expectedPairCount = EXPECTED_CLIENTS.reduce((sum, client) => sum + requestedModels(client).filter((entry) => {
  const modelId = client === 'grok' ? grokSelectorToModelId(entry) : entry.replace(/^prism\//, '')
  const model = KNOWN_MODELS.find((m) => m.id === modelId)
  return model !== undefined && !disabledProviders.has(model.provider)
}).length, 0)
if (expectedPairCount === 0) fail('no enabled requested provider pairs')
if (run.pairs.length !== expectedPairCount) fail(`run has ${run.pairs.length} pairs, want ${expectedPairCount}`)
for (const required of ['daemon/health.json', 'daemon/usage-before.json', 'daemon/usage-after.json']) {
  if (!inventory.has(required)) fail(`missing ${required}`)
}

let manifestLines = null
if (problems.some((problem) => problem === 'manifest.sha256 is not a regular file')) finish()
try { manifestLines = readFileSync(path.join(dir, 'manifest.sha256'), 'utf8').split('\n').filter((line) => line.trim() !== '') } catch { fail('missing manifest.sha256') }
if (manifestLines !== null) {
  if (manifestLines.length === 0) fail('manifest.sha256 is empty')
  const seen = new Set()
  manifestLines.forEach((line, index) => {
    const match = /^([0-9a-fA-F]{64}) [ *](.+)$/.exec(line)
    if (match === null) { fail(`malformed manifest line ${index + 1}`); return }
    let rel
    try { rel = canonicalPath(match[2]) } catch (error) { fail(`manifest line ${index + 1} has ${error.message}: ${match[2]}`); return }
    if (EXCLUDED_ROOT.has(rel)) { fail(`manifest lists excluded root file ${rel}`); return }
    if (seen.has(rel)) { fail(`duplicate manifest entry ${rel}`); return }
    seen.add(rel)
    const bytes = inventory.get(rel)
    if (bytes === undefined) fail(`manifest names missing file ${rel}`)
    else if (sha256(bytes) !== match[1].toLowerCase()) fail(`checksum mismatch for ${rel}`)
  })
  for (const rel of inventory.keys()) if (!seen.has(rel)) fail(`manifest omits ${rel}`)
}
if (problems.length > 0) finish()
const summary = {
  ok: true, runId: run.runId, featureId: run.featureId,
  pairs: run.pairs.map((p) => ({ client: p.client, modelId: p.modelId, elapsedSeconds: p.elapsedSeconds, verdict: p.verdict })),
}
writeFileSync(assertionPath, JSON.stringify(summary, null, 2), { flag: 'wx' })
console.log(JSON.stringify(summary))
