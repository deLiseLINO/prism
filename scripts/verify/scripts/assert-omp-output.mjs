import { readFileSync } from 'node:fs'

const [path, marker, ...flags] = process.argv.slice(2)
if (!path || !marker) {
  console.error('usage: assert-omp-output.mjs <ndjson-path> <final-marker> [--require-tool] [--thinking-optional]')
  process.exit(2)
}
const requireTool = flags.includes('--require-tool')
const thinkingOptional = flags.includes('--thinking-optional')
const records = readFileSync(path, 'utf8')
  .split('\n')
  .filter((line) => line.trim() !== '')
  .map((line, index) => {
    try {
      return JSON.parse(line)
    } catch {
      throw new Error(`invalid OMP NDJSON at line ${index + 1}`)
    }
  })

function values(node) {
  if (Array.isArray(node)) return node.flatMap(values)
  if (node === null || typeof node !== 'object') return []
  return [node, ...Object.values(node).flatMap(values)]
}

const objects = records.flatMap(values)
const assistantMessages = objects.filter((value) => value.role === 'assistant')
const hasMarker = assistantMessages.some((value) => JSON.stringify(value).includes(marker))
const hasThinking = objects.some((value) => {
  if (value.type === 'thinking' && typeof value.thinking === 'string') return value.thinking.trim() !== ''
  if (value.type === 'thinking_delta' && typeof value.delta === 'string') return value.delta.trim() !== ''
  return false
})
const hasToolCall = objects.some((value) =>
  ['toolCall', 'tool_call', 'tool_use', 'tool_execution_start'].includes(value.type),
)
const hasToolResult = objects.some((value) =>
  ['toolResult', 'tool_result', 'tool_use_result', 'tool_execution_end'].includes(value.type)
    || value.role === 'toolResult',
)
const error = objects.find((value) =>
  value.type === 'error'
    || value.type === 'agent_error'
    || value.stopReason === 'error'
    || typeof value.errorMessage === 'string'
    || (value.type === 'tool_execution_end' && value.isError === true),
)

if (error !== undefined) throw new Error(`OMP emitted an error: ${JSON.stringify(error)}`)
if (!hasMarker) throw new Error(`OMP final output does not contain ${marker}`)
if (!hasThinking && !thinkingOptional) throw new Error('OMP emitted no non-empty thinking block')
if (requireTool && !hasToolCall) throw new Error('OMP emitted no tool call')
if (requireTool && !hasToolResult) throw new Error('OMP emitted no tool result')

if (!hasThinking && thinkingOptional) {
  console.error('warning: OMP emitted no thinking block; antigravity suppresses thought summaries on tool turns (upstream behavior, not a prism defect)')
}
console.log(JSON.stringify({
  ok: true,
  marker,
  thinking: hasThinking,
  toolCall: hasToolCall,
  toolResult: hasToolResult,
  records: records.length,
}))
