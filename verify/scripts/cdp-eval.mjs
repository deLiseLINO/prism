// Minimal CDP evaluator: node cdp-eval.mjs <ws-url> <expression> [timeout-ms]
// Uses the built-in WebSocket (Node 22+); connects to the page target and prints the JSON result.
const [wsUrl, expression, timeoutArg] = process.argv.slice(2)
if (!wsUrl || !expression) {
  console.error('usage: cdp-eval.mjs <ws-url> <expression> [timeout-ms]')
  process.exit(2)
}
const timeoutMs = Number(timeoutArg) > 0 ? Number(timeoutArg) : 10_000

const timeout = setTimeout(() => {
  console.error('FAIL: cdp eval timed out')
  process.exit(1)
}, timeoutMs)

function wrap(expression) {
  const candidates = [
    `(async () => (${expression}))()`,
    `(async () => { ${expression} })()`,
  ]
  const trimmed = expression.trim().replace(/;+\s*$/, '')
  const split = Math.max(trimmed.lastIndexOf(';'), trimmed.lastIndexOf('\n'))
  if (split !== -1) {
    const head = trimmed.slice(0, split + 1)
    const tail = trimmed.slice(split + 1).trim()
    if (tail) candidates.splice(1, 0, `(async () => { ${head} return (${tail}) })()`)
  }
  for (const candidate of candidates) {
    try {
      new Function(`return ${candidate}`)
    } catch (error) {
      if (error instanceof SyntaxError) continue
      throw error
    }
    return candidate
  }
  return null
}

const wrapped = wrap(expression)
if (wrapped === null) {
  clearTimeout(timeout)
  console.error('FAIL: expression is neither a valid expression nor statement code')
  process.exit(1)
}

const ws = new WebSocket(wsUrl)

ws.addEventListener('open', () => {
  ws.send(JSON.stringify({ id: 1, method: 'Runtime.evaluate', params: { expression: wrapped, awaitPromise: true, returnByValue: true } }))
})

ws.addEventListener('message', (event) => {
  const msg = JSON.parse(event.data.toString())
  if (msg.id !== 1) return
  clearTimeout(timeout)
  if (msg.error) {
    console.error(`FAIL: ${JSON.stringify(msg.error)}`)
    process.exit(1)
  }
  const result = msg.result?.result
  if (result?.subtype === 'error' || msg.result?.exceptionDetails) {
    console.error(`FAIL: ${result?.description ?? JSON.stringify(msg.result?.exceptionDetails)}`)
    process.exit(1)
  }
  console.log(JSON.stringify(result?.value ?? result ?? null))
  ws.close()
  process.exit(0)
})

ws.addEventListener('error', () => {
  clearTimeout(timeout)
  console.error('FAIL: websocket error')
  process.exit(1)
})

ws.addEventListener('close', () => {
  clearTimeout(timeout)
  console.error('FAIL: websocket closed before response')
  process.exit(1)
})
