import test from 'node:test'
import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const here = path.dirname(fileURLToPath(import.meta.url))
const marker = 'PRISM_OMP_THINKING_OK'
const text = (value) => [{ type: 'text', text: value }]
const ndjson = (records) => records.map((record) => JSON.stringify(record)).join('\n') + '\n'
const invoke = (script, args) => spawnSync(process.execPath, [path.join(here, script), ...args], { encoding: 'utf8' })
function temporary(fn) {
  const root = mkdtempSync(path.join(os.tmpdir(), 'prism-verifiers-'))
  try { return fn(root) } finally { rmSync(root, { recursive: true, force: true }) }
}
const call = { type: 'tool_execution_start', toolCallId: 'read-1', toolName: 'read', args: { path: 'probe.txt' } }
const result = { type: 'tool_execution_end', toolCallId: 'read-1', toolName: 'read', isError: false, result: { content: text('PRISM_TOOL_INPUT') } }
const final = { type: 'message_end', message: { role: 'assistant', stopReason: 'stop', content: text(marker) } }
function checkOmp(records, flags = ['--require-tool', '--thinking-optional']) {
  return temporary((root) => {
    const file = path.join(root, 'transcript.ndjson')
    writeFileSync(file, typeof records === 'string' ? records : ndjson(records))
    return invoke('assert-omp-output.mjs', [file, marker, ...flags])
  })
}

test('completed read result and final text satisfy the tool proof', () => {
  const checked = checkOmp([call, result, final])
  assert.equal(checked.status, 0, checked.stderr)
  assert.equal(JSON.parse(checked.stdout).toolResult, true)
})

test('completed lifecycle accepts duplicate tool snapshots and thinking', () => {
  const announce = { type: 'message_end', message: { role: 'assistant', stopReason: 'toolUse', content: [{ type: 'thinking', thinking: 'read the local input' }, { type: 'toolCall', id: 'read-1', name: 'read', arguments: { path: 'probe.txt' } }] } }
  const snapshot = { type: 'message_end', message: { role: 'toolResult', toolCallId: 'read-1', toolName: 'read', isError: false, content: text('PRISM_TOOL_INPUT') } }
  const checked = checkOmp([{ type: 'agent_start' }, announce, call, result, snapshot, final, { type: 'agent_end', messages: [final.message] }], ['--require-tool'])
  assert.equal(checked.status, 0, checked.stderr)
  assert.equal(JSON.parse(checked.stdout).thinking, true)
})
const ompInvalid = [
  ['thinking is not final text', [call, result, { ...final, message: { ...final.message, content: [{ type: 'thinking', thinking: marker }] } }]],
  ['metadata is not final text', [call, result, { ...final, message: { ...final.message, content: text('done'), metadata: marker } }]],
  ['partial delta is not completed output', [call, result, { type: 'message_update', assistantMessageEvent: { type: 'text_delta', delta: marker } }]],
  ['tool output is not final output', [call, { ...result, result: { content: text(`PRISM_TOOL_INPUT ${marker}`) } }, { ...final, message: { ...final.message, content: text('done') } }]],
  ['failed execution cannot pass', [call, { ...result, isError: true }, final]],
  ['nested failed result cannot pass', [call, { ...result, result: { ...result.result, isError: true } }, final]],
  ['unresolved tool cannot pass', [call, final]],
  ['unmatched result cannot pass', [call, result, { ...result, toolCallId: 'other' }, final]],
  ['result after final cannot pass', [call, final, result]],
  ['wrong file cannot prove read', [{ ...call, args: { path: '../probe.txt' } }, result, final]],
  ['wrong content cannot prove read', [call, { ...result, result: { content: text('unrelated') } }, final]],
  ['aborted terminal cannot pass', [call, result, { ...final, message: { ...final.message, stopReason: 'aborted' } }]],
  ['truncated lifecycle cannot pass', [{ type: 'agent_start' }, call, result, final]],
  ['malformed input cannot pass', ndjson([call, result, final]) + '{'],
]
for (const [name, records] of ompInvalid) {
  test(name, () => {
    const checked = checkOmp(records)
    assert.notEqual(checked.status, 0)
    assert.equal(checked.stdout, '')
  })
}

const model = 'codex/gpt-5.6-luna'
const selectors = { grok: 'prism-codex-gpt-5-6-luna', omp: `prism/${model}` }
const prose = 'Local fixture detail. '.repeat(70)
function matrixFixture(root) {
  mkdirSync(path.join(root, 'daemon'))
  mkdirSync(path.join(root, 'pairs'))
  const files = new Map()
  const put = (name, bytes) => { files.set(name, bytes); writeFileSync(path.join(root, name), bytes) }
  for (const name of ['health', 'usage-before', 'usage-after', 'providers']) put(`daemon/${name}.json`, '{}')
  const run = { schema: 'prism-live-matrix-run/1', runId: '20261007-120000.1', featureId: 'client-compatibility', minSeconds: 1, grokModels: [selectors.grok], ompModels: [selectors.omp], pairs: [] }
  for (const client of ['grok', 'omp']) {
    const finalText = `${prose}PRISM_MATRIX_${client.toUpperCase()}_fixture1`
    const message = { role: 'assistant', stopReason: 'stop', content: text(finalText) }
    const stdout = client === 'grok'
      ? ndjson([{ type: 'text', data: finalText }, { type: 'usage' }, { type: 'end', stopReason: 'end_turn' }])
      : ndjson([{ type: 'agent_start' }, { type: 'message_end', message }, { type: 'agent_end', messages: [message] }])
    const pair = { client, modelId: model, provider: 'codex', account: 'fixture-account', exitCode: 0, elapsedSeconds: 1, transportErrorCount: 0, failedAttemptCount: 0, failedAttemptErrorCount: 0, attemptCount: 1, requestCount: 1, markerCount: 1, finalAssistantChars: [...finalText].length, verdict: 'pass', stdoutPath: `pairs/${client}.ndjson`, stderrPath: `pairs/${client}.stderr`, daemonLogPath: `pairs/${client}.daemon.log` }
    put(pair.stdoutPath, stdout)
    put(pair.stderrPath, '')
    put(pair.daemonLogPath, 'local fixture\n')
    run.pairs.push(pair)
  }
  const save = () => put('run.json', JSON.stringify(run))
  const manifest = () => writeFileSync(path.join(root, 'manifest.sha256'), [...files].map(([name, bytes]) => `${createHash('sha256').update(bytes).digest('hex')}  ${name}`).join('\n') + '\n')
  save()
  manifest()
  return { run, put, save, manifest }
}
test('matrix verifier derives counts from complete raw requests and verifies every evidence digest', () => temporary((root) => {
  matrixFixture(root)
  const checked = invoke('assert-matrix-run.mjs', [root])
  assert.equal(checked.status, 0, checked.stderr)
  assert.equal(JSON.parse(readFileSync(path.join(root, 'assertion.json'))).pairs.length, 2)
}))
const matrixInvalid = [
  ['omitted artifact', (root) => writeFileSync(path.join(root, 'unlisted.txt'), 'not covered')],
  ['empty manifest', (root) => writeFileSync(path.join(root, 'manifest.sha256'), '')],
  ['changed evidence', (root) => writeFileSync(path.join(root, 'pairs/grok.stderr'), 'changed')],
  ['duplicate manifest entry', (root) => { const file = path.join(root, 'manifest.sha256'); const bytes = readFileSync(file, 'utf8'); writeFileSync(file, bytes + bytes.split('\n')[0] + '\n') }],
  ['symlink artifact', (root) => symlinkSync(path.join(root, 'run.json'), path.join(root, 'linked.json'))],
  ['missing completion', (root, fixture) => { fixture.put('pairs/grok.ndjson', ndjson([{ type: 'text', data: `${prose}PRISM_MATRIX_GROK_fixture1` }])); fixture.manifest() }],
  ['raw error hidden by passing metadata', (root, fixture) => { const records = readFileSync(path.join(root, 'pairs/omp.ndjson'), 'utf8').trim().split('\n').map(JSON.parse); records.splice(1, 0, { type: 'error' }); fixture.put('pairs/omp.ndjson', ndjson(records)); fixture.manifest() }],
  ['failed attempts hidden by passing verdict', (root, fixture) => { fixture.run.pairs[0].failedAttemptCount = 1; fixture.save(); fixture.manifest() }],
  ['claimed request count differs from raw', (root, fixture) => { fixture.run.pairs[0].requestCount = 2; fixture.save(); fixture.manifest() }],
  ['claimed output count differs from raw', (root, fixture) => { fixture.run.pairs[1].finalAssistantChars += 1; fixture.save(); fixture.manifest() }],
  ['duplicate pairs', (root, fixture) => { fixture.run.pairs.push(fixture.run.pairs[0]); fixture.save(); fixture.manifest() }],
  ['unsafe manifest path', (root) => writeFileSync(path.join(root, 'manifest.sha256'), `${'0'.repeat(64)}  ../outside\n`)],
]
for (const [name, alter] of matrixInvalid) {
  test(`matrix rejects ${name} and clears its previous assertion`, () => temporary((root) => {
    const fixture = matrixFixture(root)
    writeFileSync(path.join(root, 'assertion.json'), '{"ok":true}')
    alter(root, fixture)
    const checked = invoke('assert-matrix-run.mjs', [root])
    assert.notEqual(checked.status, 0)
    assert.equal(checked.stdout, '')
    assert.equal(existsSync(path.join(root, 'assertion.json')), false)
  }))
}
