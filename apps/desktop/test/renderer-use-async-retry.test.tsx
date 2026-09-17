// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createRoot, type Root } from 'react-dom/client'
import { act } from 'react'
import { createElement, type ReactNode } from 'react'
import { useAsync, type AsyncState } from '../renderer/src/useAsync'

let container: HTMLDivElement | null = null
let root: Root | null = null

beforeEach(() => {
  ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
  container = document.createElement('div')
  root = createRoot(container)
})

afterEach(() => {
  act(() => { root?.unmount() })
  root = null
  container = null
})

async function settle(): Promise<void> {
  await act(async () => { await vi.advanceTimersByTimeAsync(1_200) })
}

function renderProbe(load: () => Promise<string>, onState: (s: AsyncState<string>) => void): void {
  act(() => {
    root?.render(createElement(Probe, { load, onState }))
  })
}

function Probe({ load, onState }: { load: () => Promise<string>; onState: (s: AsyncState<string>) => void }): ReactNode {
  const { state } = useAsync(load, [])
  onState(state)
  return null
}

describe('useAsync error retry', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('retries on its own after a failure until the load answers', async () => {
    let attempts = 0
    const load = () => {
      attempts++
      if (attempts < 3) return Promise.reject(new Error('daemon not up yet'))
      return Promise.resolve('accounts')
    }
    const states: AsyncState<string>[] = []
    renderProbe(load, (s) => states.push(s))

    await settle()
    await settle()

    expect(attempts).toBe(3)
    expect(states.at(-1)?.kind).toBe('ready')
    expect(states.at(-1)?.kind === 'ready' ? states.at(-1).value : '').toBe('accounts')
  })

  it('stops retrying once the first value arrived', async () => {
    let attempts = 0
    const load = () => {
      attempts++
      return Promise.resolve('accounts')
    }
    const states: AsyncState<string>[] = []
    renderProbe(load, (s) => states.push(s))

    for (let i = 0; i < 8; i++) await settle()

    expect(attempts).toBe(1)
    expect(states.at(-1)?.kind).toBe('ready')
  })

  it('gives up retrying after the attempt budget is spent', async () => {
    let attempts = 0
    const load = () => {
      attempts++
      return Promise.reject(new Error('down'))
    }
    const states: AsyncState<string>[] = []
    renderProbe(load, (s) => states.push(s))

    for (let i = 0; i < 32; i++) await settle()
    const bounded = attempts
    expect(bounded).toBeLessThanOrEqual(8)
    expect(bounded).toBeGreaterThan(1)

    for (let i = 0; i < 6; i++) await settle()

    expect(attempts).toBe(bounded)
    expect(states.at(-1)?.kind).toBe('error')
  })
})
