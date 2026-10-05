import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { createServer } from 'node:http'
import { fileURLToPath } from 'node:url'

const script = fileURLToPath(new URL('./cdp-ws.mjs', import.meta.url))
const renderer = 'file:///tmp/prism-renderer/index.html'
let targets = []
const server = createServer((request, response) => {
  assert.equal(request.url, '/json')
  response.setHeader('Content-Type', 'application/json')
  response.end(JSON.stringify(targets))
})
await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve))
const port = server.address().port
const websocket = `ws://127.0.0.1:${port}/devtools/page/owned`
const page = (url, ws = websocket) => ({ type: 'page', url, webSocketDebuggerUrl: ws })

function discover() {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [script, String(port), renderer])
    let stdout = ''
    let stderr = ''
    child.stdout.on('data', (chunk) => { stdout += chunk })
    child.stderr.on('data', (chunk) => { stderr += chunk })
    child.on('error', reject)
    child.on('close', (code) => resolve({ code, stdout: stdout.trim(), stderr }))
  })
}

try {
  targets = [page('file:///tmp/foreign/index.html')]
  let result = await discover()
  assert.notEqual(result.code, 0, 'foreign renderer must be refused')
  assert.match(result.stderr, /renderer|page target/)

  targets = [page(renderer), page(`${renderer}#/overview`)]
  result = await discover()
  assert.notEqual(result.code, 0, 'ambiguous renderers must be refused')

  targets = [page(renderer, `ws://127.0.0.1:${port + 1}/devtools/page/foreign`)]
  result = await discover()
  assert.notEqual(result.code, 0, 'foreign WebSocket endpoint must be refused')

  targets = [{ type: 'worker', url: renderer, webSocketDebuggerUrl: websocket }, page(`${renderer}#/overview`)]
  result = await discover()
  assert.equal(result.code, 0, result.stderr)
  assert.equal(result.stdout, websocket)
  console.log('CDP renderer selection proof passed')
} finally {
  await new Promise((resolve) => server.close(resolve))
}
