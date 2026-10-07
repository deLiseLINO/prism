export function parseNdjson(source, label) {
  const entries = []
  source.split('\n').forEach((line, index) => {
    if (line.trim() === '') return
    let record
    try {
      record = JSON.parse(line)
    } catch {
      throw new Error(`invalid ${label} NDJSON at line ${index + 1}`)
    }
    if (record === null || typeof record !== 'object' || Array.isArray(record)) {
      throw new Error(`invalid ${label} NDJSON at line ${index + 1}: record is not an object`)
    }
    entries.push({ line: index + 1, record })
  })
  return entries
}

const isObject = (value) => value !== null && typeof value === 'object' && !Array.isArray(value)

function joinText(content) {
  if (!Array.isArray(content)) return ''
  return content.filter((block) => isObject(block) && block.type === 'text' && typeof block.text === 'string')
    .map((block) => block.text).join('')
}

function messageFailure(message) {
  if (!isObject(message)) return null
  if (message.role === 'assistant') {
    if (message.stopReason === 'error' || message.stopReason === 'aborted') return `assistant stopReason ${message.stopReason}`
    if (typeof message.errorMessage === 'string' && message.errorMessage.trim() !== '') return 'assistant errorMessage present'
  }
  if (message.role === 'toolResult' && message.isError === true) return `tool result ${message.toolCallId ?? ''} failed`
  return null
}

function assistantIsFinal(message) {
  if (message.stopReason !== 'stop' || !Array.isArray(message.content)) return false
  if (message.content.some((block) => isObject(block) && (block.type === 'toolCall' || block.type === 'tool_use'))) return false
  return joinText(message.content).trim() !== ''
}

export function parseOmpTranscript(source, { label = 'OMP' } = {}) {
  return parseOmpEntries(parseNdjson(source, label))
}

export function parseOmpEntries(entries) {
  const calls = []
  const results = []
  const errors = []
  let thinking = false
  let lastAssistant = null
  let lifecycleOpen = false
  let lifecycleSeen = false

  const flag = (index, reason) => errors.push({ recordIndex: index, line: entries[index].line, reason })
  const checkSnapshot = (index, message) => {
    const failure = messageFailure(message)
    if (failure !== null) flag(index, failure)
  }

  entries.forEach(({ record }, index) => {
    switch (record.type) {
      case 'agent_start':
        if (lifecycleOpen) flag(index, 'nested agent_start')
        lifecycleSeen = true
        lifecycleOpen = true
        lastAssistant = null
        break
      case 'error':
      case 'agent_error':
        flag(index, `${record.type} record`)
        break
      case 'message_update': {
        const event = record.assistantMessageEvent
        if (!isObject(event)) break
        if (event.type === 'error') flag(index, 'nested stream error')
        if (event.type === 'thinking_delta' && typeof event.delta === 'string' && event.delta.trim() !== '') thinking = true
        break
      }
      case 'message_end': {
        const message = record.message
        if (!isObject(message)) {
          flag(index, 'message_end has no message')
          break
        }
        checkSnapshot(index, message)
        if (message.role === 'assistant') {
          lastAssistant = { recordIndex: index, message }
          if (Array.isArray(message.content)) {
            for (const block of message.content) {
              if (isObject(block) && block.type === 'thinking' && typeof block.thinking === 'string' && block.thinking.trim() !== '') thinking = true
            }
          }
        } else if (message.role === 'toolResult') {
          results.push({ id: message.toolCallId, name: message.toolName, text: joinText(message.content), isError: message.isError, recordIndex: index })
        }
        break
      }
      case 'tool_execution_start':
        calls.push({ id: record.toolCallId, name: record.toolName, args: record.args, recordIndex: index })
        break
      case 'tool_execution_end': {
        if (record.isError === true || record.result?.isError === true) flag(index, `tool ${record.toolCallId ?? ''} execution failed`)
        results.push({ id: record.toolCallId, name: record.toolName, text: isObject(record.result) ? joinText(record.result.content) : '', isError: record.isError, recordIndex: index })
        break
      }
      case 'turn_end':
        checkSnapshot(index, record.message)
        if (Array.isArray(record.toolResults)) record.toolResults.forEach((item) => checkSnapshot(index, item))
        break
      case 'agent_end':
        if (lifecycleSeen && !lifecycleOpen) flag(index, 'agent_end without agent_start')
        lifecycleOpen = false
        if (Array.isArray(record.messages)) record.messages.forEach((item) => checkSnapshot(index, item))
        break
      default:
    }
  })

  if (lifecycleOpen && entries.length > 0) flag(entries.length - 1, 'incomplete invocation: no agent_end')
  const final = lastAssistant !== null && assistantIsFinal(lastAssistant.message)
    ? { recordIndex: lastAssistant.recordIndex, text: joinText(lastAssistant.message.content) }
    : null
  return { records: entries.length, thinking, final, calls, results, errors }
}
