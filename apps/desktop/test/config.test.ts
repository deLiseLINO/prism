import { afterEach, describe, expect, it, vi } from 'vitest'
import net from 'node:net'
import { loadDesktopConfig, PRISM_PORT_ENV } from '../main/config'

const originalPort = process.env[PRISM_PORT_ENV]

afterEach(() => {
  if (originalPort === undefined) delete process.env[PRISM_PORT_ENV]
  else process.env[PRISM_PORT_ENV] = originalPort
  vi.restoreAllMocks()
})

describe('loadDesktopConfig', () => {
  it('uses the default port without PRISM_PORT', async () => {
    delete process.env[PRISM_PORT_ENV]
    const config = await loadDesktopConfig()
    expect(config.port).toBe(10200)
  })

  it('keeps an explicit port', async () => {
    process.env[PRISM_PORT_ENV] = '12345'
    const config = await loadDesktopConfig()
    expect(config.port).toBe(12345)
  })

  it('selects the next free port when the first auto candidate is taken', async () => {
    process.env[PRISM_PORT_ENV] = 'auto'
    const canBind = (port: number) => new Promise<boolean>((resolve) => {
      const probe = net.createServer()
      probe.once('error', () => resolve(false))
      probe.listen(port, '127.0.0.1', () => probe.close(() => resolve(true)))
    })
    let blocked = -1
    for (let candidate = 10201; candidate < 10250; candidate++) {
      if (await canBind(candidate) && await canBind(candidate + 1)) {
        blocked = candidate
        break
      }
    }
    if (blocked === -1) {
      throw new Error('no adjacent free port pair in the auto range for this test')
    }
    const blocker = net.createServer()
    await new Promise<void>((resolve) => blocker.listen(blocked, '127.0.0.1', resolve))
    const config = await loadDesktopConfig()
    expect(config.port).toBe(blocked + 1)
    await new Promise<void>((resolve) => blocker.close(() => resolve()))
  })
})
