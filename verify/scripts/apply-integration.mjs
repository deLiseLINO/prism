const [wsUrl, integrationId] = process.argv.slice(2)
if (!wsUrl || !integrationId) {
  console.error('usage: apply-integration.mjs <ws-url> <integration-id>')
  process.exit(2)
}

const timeout = setTimeout(() => {
  console.error(`FAIL: ${integrationId} Apply timed out`)
  process.exit(1)
}, 15_000)

const ws = new WebSocket(wsUrl)

const expression = `(async () => {
  const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
  const nav = [...document.querySelectorAll('nav button')].find((node) => node.textContent.includes('Integrations'))
  if (!nav) throw new Error('Integrations navigation button not found')
  nav.click()
  for (let i = 0; i < 40; i++) {
    if (document.querySelector('main h1')?.textContent.trim() === 'Integrations' && document.querySelectorAll('.int-row-wrap').length >= 3) break
    await sleep(100)
  }
  if (document.querySelector('main h1')?.textContent.trim() !== 'Integrations') throw new Error('Integrations view did not open')
  const card = [...document.querySelectorAll('.int-row-wrap')].find((node) => node.querySelector('.int-name')?.textContent.trim() === '${integrationId}')
  if (!card) throw new Error('${integrationId} card not found')
  const button = [...card.querySelectorAll('button')].find((node) => node.textContent.trim() === 'Apply')
  if (!button) throw new Error('${integrationId} Apply button not found')
  if (button.disabled) throw new Error('${integrationId} Apply button is disabled')
  button.click()
  for (let i = 0; i < 60; i++) {
    await sleep(100)
    const current = [...document.querySelectorAll('.int-row-wrap')].find((node) => node.querySelector('.int-name')?.textContent.trim() === '${integrationId}')
    const alert = current?.querySelector('[role=alert]')
    if (alert) throw new Error(alert.textContent)
    const state = current?.querySelector('.int-status')?.textContent.trim()
    if (state === 'managed') return {id: '${integrationId}', state}
  }
  throw new Error('${integrationId} did not become managed after clicking Apply')
})()`

ws.addEventListener('open', () => {
  ws.send(JSON.stringify({ id: 1, method: 'Runtime.evaluate', params: { expression, awaitPromise: true, returnByValue: true } }))
})

ws.addEventListener('message', (event) => {
  const message = JSON.parse(event.data.toString())
  if (message.id !== 1) return
  clearTimeout(timeout)
  if (message.error) {
    console.error(`FAIL: ${JSON.stringify(message.error)}`)
    process.exit(1)
  }
  const result = message.result?.result
  if (result?.subtype === 'error' || message.result?.exceptionDetails) {
    console.error(`FAIL: ${result?.description ?? JSON.stringify(message.result?.exceptionDetails)}`)
    process.exit(1)
  }
  const value = result?.value
  if (value?.state === 'managed') {
    console.log(JSON.stringify(value))
    ws.close()
    process.exit(0)
  }
  console.error(`FAIL: unexpected Apply result: ${JSON.stringify(value ?? null)}`)
  process.exit(1)
})

ws.addEventListener('error', () => {
  clearTimeout(timeout)
  console.error('FAIL: websocket error')
  process.exit(1)
})
