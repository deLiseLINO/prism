import { readFileSync } from 'node:fs'
import path from 'node:path'
import { parseOmpTranscript } from './omp-transcript.mjs'

const [file, marker, ...flags] = process.argv.slice(2)
if (!file || !marker) {
  console.error('usage: assert-omp-output.mjs <ndjson-path> <final-marker> [--require-tool] [--thinking-optional]')
  process.exit(2)
}
const requireTool = flags.includes('--require-tool')
const thinkingOptional = flags.includes('--thinking-optional')

const PROBE = 'probe.txt'
const SENTINEL = 'PRISM_TOOL_INPUT'

function probePath(value) {
  if (typeof value !== 'string' || value === '' || value.includes('\0')) return false
  const parts = value.split('/')
  if (parts.includes('..')) return false
  if (path.posix.isAbsolute(value)) return parts[parts.length - 1] === PROBE
  return value === PROBE || value === `./${PROBE}`
}

function requireReadProof(facts, finalIndex) {
  const executions = new Map()
  for (const call of facts.calls) {
    if (typeof call.id !== 'string' || call.id === '') throw new Error('OMP tool call has no id')
    const known = executions.get(call.id)
    if (known === undefined) {
      executions.set(call.id, { call, results: [] })
    } else if (known.call.name !== call.name || JSON.stringify(known.call.args) !== JSON.stringify(call.args)) {
      throw new Error(`OMP tool call ${call.id} has conflicting starts`)
    }
  }
  for (const result of facts.results) {
    if (typeof result.id !== 'string' || result.id === '') throw new Error('OMP tool result has no id')
    const execution = executions.get(result.id)
    if (execution === undefined) throw new Error(`OMP tool result ${result.id} matches no executed tool call`)
    if (result.recordIndex < execution.call.recordIndex) throw new Error(`OMP tool result ${result.id} precedes its call start`)
    if (result.recordIndex > finalIndex) throw new Error(`OMP tool result ${result.id} arrives after the final answer`)
    if (result.name !== execution.call.name) throw new Error(`OMP tool result ${result.id} conflicts with its call name`)
    if (result.isError !== false) throw new Error(`OMP tool result ${result.id} is not a successful result`)
    const first = execution.results[0]
    if (first !== undefined && first.text !== result.text) throw new Error(`OMP tool result ${result.id} has conflicting content`)
    execution.results.push(result)
  }
  let proven = false
  for (const [id, execution] of executions) {
    if (execution.call.recordIndex > finalIndex) throw new Error(`OMP tool call ${id} starts after the final answer`)
    if (execution.results.length === 0) throw new Error(`OMP tool call ${id} is unresolved at the final answer`)
    const isProbe = execution.call.name === 'read' && probePath(execution.call.args?.path)
    if (isProbe && execution.results[0].text.includes(SENTINEL)) proven = true
  }
  if (!proven) {
    const read = [...executions.values()].some((e) => e.call.name === 'read' && probePath(e.call.args?.path))
    throw new Error(read
      ? `OMP read result for ${PROBE} does not contain ${SENTINEL}`
      : `OMP emitted no successful read of ${PROBE}`)
  }
}

const facts = parseOmpTranscript(readFileSync(file, 'utf8'), { label: 'OMP' })
if (facts.errors.length > 0) {
  const first = facts.errors[0]
  throw new Error(`OMP emitted an error at line ${first.line}: ${first.reason}`)
}
if (facts.final === null) throw new Error('OMP has no completed final assistant text')
if (!facts.final.text.includes(marker)) throw new Error(`OMP final output does not contain ${marker}`)
if (!facts.thinking && !thinkingOptional) throw new Error('OMP emitted no non-empty thinking block')
if (requireTool) requireReadProof(facts, facts.final.recordIndex)

if (!facts.thinking && thinkingOptional) console.error('warning: OMP emitted no thinking block')
console.log(JSON.stringify({
  ok: true,
  marker,
  thinking: facts.thinking,
  toolCall: facts.calls.length > 0,
  toolResult: facts.results.length > 0,
  records: facts.records,
}))
