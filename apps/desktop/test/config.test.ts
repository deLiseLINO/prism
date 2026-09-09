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

  it('selects the first free port for auto', async () => {
    process.env[PRISM_PORT_ENV] = 'auto'
    const blocker = net.createServer()
    await new Promise<void>((resolve) => blocker.listen(10201, '127.0.0.1', resolve))
    const config = await loadDesktopConfig()
    expect(config.port).toBe(10202)
    await new Promise<void>((resolve) => blocker.close(() => resolve()))
  })
})
