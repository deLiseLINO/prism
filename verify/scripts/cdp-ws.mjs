// Pick the page target's WebSocket URL from a CDP /json listing: node cdp-ws.mjs <cdp-port>
// Electron can expose extra targets (shared workers, devtools); only type=="page" is the renderer.
const [cdpPort] = process.argv.slice(2)
if (!cdpPort) {
  console.error('usage: cdp-ws.mjs <cdp-port>')
  process.exit(2)
}

let targets
try {
  const response = await fetch(`http://127.0.0.1:${cdpPort}/json`)
  if (!response.ok) {
    console.error(`FAIL: CDP /json answered ${response.status}`)
    process.exit(1)
  }
  targets = await response.json()
} catch {
  console.error(`FAIL: cannot reach CDP on port ${cdpPort}`)
  process.exit(1)
}
const page = targets.find((target) => target.type === 'page' && target.webSocketDebuggerUrl)
if (!page) {
  console.error('FAIL: no page target in CDP listing')
  process.exit(1)
}
console.log(page.webSocketDebuggerUrl)
