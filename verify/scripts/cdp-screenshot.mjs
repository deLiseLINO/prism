import { writeFileSync } from 'node:fs'

const [wsUrl, outputPath] = process.argv.slice(2)
if (!wsUrl || !outputPath) {
  console.error('usage: cdp-screenshot.mjs <ws-url> <output-path>')
  process.exit(2)
}

const timeout = setTimeout(() => {
  console.error('FAIL: CDP screenshot timed out')
  process.exit(1)
}, 10_000)
const ws = new WebSocket(wsUrl)
let nextId = 1

function send(method, params = {}) {
  const id = nextId++
  ws.send(JSON.stringify({ id, method, params }))
  return id
}

ws.addEventListener('open', () => send('Page.captureScreenshot', { format: 'png', captureBeyondViewport: true }))
ws.addEventListener('message', (event) => {
  const message = JSON.parse(event.data.toString())
  if (message.id !== 1) return
  clearTimeout(timeout)
  if (message.error || typeof message.result?.data !== 'string') {
    console.error(`FAIL: ${JSON.stringify(message.error ?? message.result)}`)
    process.exit(1)
  }
  writeFileSync(outputPath, Buffer.from(message.result.data, 'base64'))
  ws.close()
  process.exit(0)
})
ws.addEventListener('close', () => {
  clearTimeout(timeout)
  console.error('FAIL: websocket closed before response')
  process.exit(1)
})
ws.addEventListener('error', () => {
  clearTimeout(timeout)
  console.error('FAIL: CDP websocket error')
  process.exit(1)
})
