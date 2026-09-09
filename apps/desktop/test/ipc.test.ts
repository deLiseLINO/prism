import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { IpcChannel } from '@prism/contracts'

type Handler = (event: unknown, input: unknown) => unknown

interface FakeEvent {
  readonly sender: { isDestroyed: () => boolean } | null
}

const electronRegistry = {
  handlers: new Map<string, Handler>(),
  openExternal: vi.fn(),
  setTitleBarOverlay: vi.fn(),
}

vi.mock('electron', () => ({
  BrowserWindow: {
    fromWebContents: () => ({
      isDestroyed: () => false,
      setTitleBarOverlay: electronRegistry.setTitleBarOverlay,
    }),
  },
  ipcMain: {
    handle: (channel: string, handler: Handler) => {
      electronRegistry.handlers.set(channel, handler)
    },
  },
  shell: {
    openExternal: (url: string) => electronRegistry.openExternal(url),
  },
}))

function fakeEvent(destroyed = false): FakeEvent {
  return { sender: { isDestroyed: () => destroyed } }
}

describe('shell.openExternal IPC forwarding', () => {
  beforeEach(() => {
    electronRegistry.handlers.clear()
    electronRegistry.openExternal.mockReset()
    electronRegistry.openExternal.mockResolvedValue(undefined)
    electronRegistry.setTitleBarOverlay.mockReset()
    // Reset so each test re-registers handlers with a fresh module instance.
    vi.resetModules()
  })

  afterEach(() => {
    vi.resetModules()
  })

  it('registers a handler on the windowSetTheme channel', async () => {
    const ipcModule = await import('../main/ipc')
    ipcModule.registerIpc({
      supervisor: {} as never,
      management: {} as never,
      integrations: {} as never,
    })
    expect(electronRegistry.handlers.has(IpcChannel.shellOpenExternal)).toBe(true)
  })

  it('refuses non-http(s) urls without invoking shell.openExternal', async () => {
    const ipcModule = await import('../main/ipc')
    ipcModule.registerIpc({
      supervisor: {} as never,
      management: {} as never,
      integrations: {} as never,
    })
    const handler = electronRegistry.handlers.get(IpcChannel.shellOpenExternal)
    expect(handler).toBeDefined()
    await expect(handler!(fakeEvent(), 'file:///etc/passwd')).rejects.toThrow(/non-http\(s\)/)
    expect(electronRegistry.openExternal).not.toHaveBeenCalled()
  })

  it('refuses empty input and rejects destroyed senders', async () => {
    const ipcModule = await import('../main/ipc')
    ipcModule.registerIpc({
      supervisor: {} as never,
      management: {} as never,
      integrations: {} as never,
    })
    const handler = electronRegistry.handlers.get(IpcChannel.shellOpenExternal)
    expect(handler).toBeDefined()
    await expect(handler!(fakeEvent(), '')).rejects.toThrow(/non-empty url/)
    expect(() => handler!(fakeEvent(true), 'https://example.com')).toThrow(/untrusted sender/)
    expect(electronRegistry.openExternal).not.toHaveBeenCalled()
  })

  it('registers a handler on the windowSetTheme channel', async () => {
    const ipcModule = await import('../main/ipc')
    ipcModule.registerIpc({
      supervisor: {} as never,
      management: {} as never,
      integrations: {} as never,
    })
    expect(electronRegistry.handlers.has(IpcChannel.windowSetTheme)).toBe(true)
  })

  it('updates the overlay symbol color for a valid theme', async () => {
    const originalPlatform = process.platform
    Object.defineProperty(process, 'platform', { value: 'linux', configurable: true })
    const ipcModule = await import('../main/ipc')
    ipcModule.registerIpc({
      supervisor: {} as never,
      management: {} as never,
      integrations: {} as never,
    })
    const handler = electronRegistry.handlers.get(IpcChannel.windowSetTheme)
    try {
      await handler!(fakeEvent(), 'light')
      expect(electronRegistry.setTitleBarOverlay).toHaveBeenCalled()
    } finally {
      Object.defineProperty(process, 'platform', { value: originalPlatform, configurable: true })
    }
  })

  it('refuses an invalid window theme', async () => {
    const ipcModule = await import('../main/ipc')
    ipcModule.registerIpc({
      supervisor: {} as never,
      management: {} as never,
      integrations: {} as never,
    })
    const handler = electronRegistry.handlers.get(IpcChannel.windowSetTheme)
    await expect(Promise.resolve().then(() => handler!(fakeEvent(), 'system'))).rejects.toThrow(/dark or light/)
    expect(electronRegistry.setTitleBarOverlay).not.toHaveBeenCalled()
  })

  it('opens a valid https url via shell.openExternal', async () => {
    const ipcModule = await import('../main/ipc')
    ipcModule.registerIpc({
      supervisor: {} as never,
      management: {} as never,
      integrations: {} as never,
    })
    const handler = electronRegistry.handlers.get(IpcChannel.shellOpenExternal)
    await handler!(fakeEvent(), 'https://auth.openai.com/oauth/authorize?x=1')
    expect(electronRegistry.openExternal).toHaveBeenCalledWith(
      'https://auth.openai.com/oauth/authorize?x=1',
    )
  })

  it('forwards shell.openExternal failures as a rejected promise', async () => {
    electronRegistry.openExternal.mockRejectedValueOnce(new Error('no handler registered'))
    const ipcModule = await import('../main/ipc')
    ipcModule.registerIpc({
      supervisor: {} as never,
      management: {} as never,
      integrations: {} as never,
    })
    const handler = electronRegistry.handlers.get(IpcChannel.shellOpenExternal)
    await expect(handler!(fakeEvent(), 'https://example.com/oauth')).rejects.toThrow(
      /no handler registered/,
    )
  })
})