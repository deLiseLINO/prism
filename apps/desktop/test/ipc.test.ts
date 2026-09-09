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

describe('updater IPC forwarding', () => {
  const updater = {
    status: { state: 'idle', currentVersion: '1.0.0' } as never,
    check: vi.fn().mockResolvedValue(undefined),
    install: vi.fn(),
  }

  async function register(): Promise<void> {
    const ipcModule = await import('../main/ipc')
    ipcModule.registerIpc({
      supervisor: {} as never,
      management: {} as never,
      integrations: {} as never,
      updater,
    })
  }

  beforeEach(() => {
    electronRegistry.handlers.clear()
    updater.check.mockClear()
    updater.install.mockClear()
    vi.resetModules()
  })

  afterEach(() => {
    vi.resetModules()
  })

  it('registers handlers on the updater channels', async () => {
    await register()
    expect(electronRegistry.handlers.has(IpcChannel.updaterGetStatus)).toBe(true)
    expect(electronRegistry.handlers.has(IpcChannel.updaterCheck)).toBe(true)
    expect(electronRegistry.handlers.has(IpcChannel.updaterInstall)).toBe(true)
  })

  it('returns the updater status snapshot', async () => {
    await register()
    const handler = electronRegistry.handlers.get(IpcChannel.updaterGetStatus)
    expect(handler!(fakeEvent())).toEqual(updater.status)
  })

  it('rejects destroyed senders on every updater channel', async () => {
    await register()
    for (const channel of [IpcChannel.updaterGetStatus, IpcChannel.updaterCheck, IpcChannel.updaterInstall]) {
      const handler = electronRegistry.handlers.get(channel)
      expect(() => handler!(fakeEvent(true))).toThrow(/untrusted sender/)
    }
    expect(updater.check).not.toHaveBeenCalled()
    expect(updater.install).not.toHaveBeenCalled()
  })

  it('forwards check and install to the service', async () => {
    await register()
    await electronRegistry.handlers.get(IpcChannel.updaterCheck)!(fakeEvent())
    expect(updater.check).toHaveBeenCalledTimes(1)
    expect(() => electronRegistry.handlers.get(IpcChannel.updaterInstall)!(fakeEvent())).not.toThrow()
    expect(updater.install).toHaveBeenCalledTimes(1)
  })
})

describe('shell.openExternal IPC forwarding', () => {
  beforeEach(() => {
    electronRegistry.handlers.clear()
    electronRegistry.openExternal.mockReset()
    electronRegistry.openExternal.mockResolvedValue(undefined)
    electronRegistry.setTitleBarOverlay.mockReset()
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