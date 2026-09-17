import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

type RoutingModule = typeof import('../renderer/src/routing')

interface LocationStub {
  hash: string
  path: string
}

function installLocation(initialHash: string): { location: LocationStub; restore: () => void } {
  const stub: LocationStub = { hash: initialHash, path: '/' }
  const originalLocation = (globalThis as Record<string, unknown>)['location']
  Object.defineProperty(globalThis, 'location', {
    configurable: true,
    value: stub,
    writable: true,
  })
  Object.defineProperty(globalThis, 'window', {
    configurable: true,
    value: {
      location: stub,
      history,
      dispatchEvent: () => true,
      localStorage: {
        getItem: () => null,
        setItem: () => undefined,
      },
    },
    writable: true,
  })
  return {
    location: stub,
    restore: () => {
      Object.defineProperty(globalThis, 'location', {
        configurable: true,
        value: originalLocation,
        writable: true,
      })
    },
  }
}

const history = {
  replaceState: () => undefined,
}

describe('renderer routing', () => {
  let routing: RoutingModule
  let locationStub: LocationStub

  beforeEach(async () => {
    const installed = installLocation('#/overview')
    locationStub = installed.location
    // `window` is reassigned per-test so the module reads the fresh stub.
    routing = await import('../renderer/src/routing')
  })

  it('places stats in the nav after usage under Operate', () => {
    const entries = routing.VIEWS.map((entry) => entry.view)
    expect(entries.indexOf('stats')).toBe(entries.indexOf('usage') + 1)
    expect(routing.VIEWS[entries.indexOf('stats')].section).toBe('Operate')
  })

  afterEach(() => {
    vi.resetModules()
  })

  it('hashes each view distinctly', () => {
    expect(routing.hashFor('overview')).toBe('#/overview')
    expect(routing.hashFor('providers')).toBe('#/providers')
    expect(routing.hashFor('usage')).toBe('#/usage')
    expect(routing.hashFor('integrations')).toBe('#/integrations')
    expect(routing.hashFor('machines')).toBe('#/machines')
    expect(routing.hashFor('stats')).toBe('#/stats')
    expect(routing.hashFor('logs')).toBe('#/logs')
  })

  it('falls back to overview for unknown hashes', () => {
    expect(routing.viewFromHash('#/not-a-real-view')).toBe('overview')
    expect(routing.viewFromHash('')).toBe('overview')
  })

  it('falls back to overview for the removed #/update hash', () => {
    expect(routing.viewFromHash('#/update')).toBe('overview')
    expect(routing.VIEWS.some((entry) => entry.view === 'update')).toBe(false)
  })

  it('falls back to overview for the removed #/daemon hash', () => {
    expect(routing.viewFromHash('#/daemon')).toBe('overview')
    expect(routing.VIEWS.some((entry) => entry.view === 'daemon')).toBe(false)
  })

  it('reads the current view from location.hash', () => {
    locationStub.hash = '#/stats'
    expect(routing.readCurrentView()).toBe('stats')
    locationStub.hash = '#/logs'
    expect(routing.readCurrentView()).toBe('logs')
  })

  it('lists every workflow in the nav registry', () => {
    const seen = new Set<string>()
    for (const entry of routing.VIEWS) {
        seen.add(entry.view)
    }
    expect(seen.has('overview')).toBe(true)
    expect(seen.has('providers')).toBe(true)
    expect(seen.has('usage')).toBe(true)
    expect(seen.has('integrations')).toBe(true)
    expect(seen.has('machines')).toBe(true)
    expect(seen.has('stats')).toBe(true)
    expect(seen.has('logs')).toBe(true)

  })

  it('restores the last view on a clean hash and falls back to overview otherwise', () => {
    const store = new Map<string, string>()
    const windowStub = (globalThis as Record<string, unknown>)['window']
    Object.defineProperty(globalThis, 'window', {
      configurable: true,
      value: {
        ...((windowStub as object) ?? {}),
        localStorage: {
          getItem: (key: string) => store.get(key) ?? null,
          setItem: (key: string, value: string) => void store.set(key, value),
        },
      },
      writable: true,
    })

    locationStub.hash = ''
    expect(routing.readCurrentView()).toBe('overview')

    store.set('prism-view', 'stats')
    expect(routing.readCurrentView()).toBe('stats')

    routing.rememberView('logs')
    expect(store.get('prism-view')).toBe('logs')
    locationStub.hash = ''
    expect(routing.readCurrentView()).toBe('logs')

    store.set('prism-view', 'bogus')
    locationStub.hash = ''
    expect(routing.readCurrentView()).toBe('overview')

    store.set('prism-view', 'machines')
    locationStub.hash = ''
    expect(routing.readCurrentView()).toBe('machines')
  })
})
