const [cdpPort, expectedRenderer] = process.argv.slice(2)
const port = Number(cdpPort)
if (!Number.isInteger(port) || port < 1 || port > 65535) {
  console.error('usage: cdp-ws.mjs <cdp-port> [expected-renderer-url]')
  process.exit(2)
}

function withoutHash(value) {
  const url = new URL(value)
  url.hash = ''
  return url.href
}

try {
  const response = await fetch(`http://127.0.0.1:${port}/json`, { signal: AbortSignal.timeout(5000) })
  if (!response.ok) throw new Error(`CDP /json answered ${response.status}`)
  const targets = await response.json()
  if (!Array.isArray(targets)) throw new Error('CDP listing is not an array')
  const pages = targets.filter((target) => {
    if (target?.type !== 'page' || typeof target.webSocketDebuggerUrl !== 'string') return false
    if (expectedRenderer && (typeof target.url !== 'string' || withoutHash(target.url) !== withoutHash(expectedRenderer))) return false
    return true
  })
  if (pages.length !== 1) throw new Error(`expected exactly one renderer page target, got ${pages.length}`)
  const websocket = new URL(pages[0].webSocketDebuggerUrl)
  if (websocket.protocol !== 'ws:' || websocket.hostname !== '127.0.0.1' || Number(websocket.port) !== port) {
    throw new Error('renderer WebSocket endpoint does not match the requested CDP listener')
  }
  console.log(websocket.href)
} catch (error) {
  console.error(`FAIL: ${error.message}`)
  process.exit(1)
}
