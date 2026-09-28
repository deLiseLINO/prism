// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import type { ManagementCall, ManagementReply, ProviderView, UsageAccountView } from '@prism/contracts'
import { UsagePanel } from '../renderer/src/views/UsageView'

const quota = { used: 0, windowEnd: '', source: 'unknown' as const }
const row = (account: string, provider = 'codex'): UsageAccountView => ({ account, provider, state: 'active', quota })
const provider = (id: string, pinnedAccount?: string): ProviderView => ({
  id, wire: 'codex', models: ['m1'], credential: { state: 'set' },
  ...(pinnedAccount === undefined ? {} : { pool: { pinnedAccount } }),
})

let root: Root
let element: HTMLDivElement
let accounts: UsageAccountView[]
let providers: ProviderView[]
let calls: ManagementCall[]

beforeEach(() => {
  ;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true
  element = document.createElement('div')
  document.body.appendChild(element)
  root = createRoot(element)
  accounts = [row('codex:a')]
  providers = [provider('codex')]
  calls = []
  Object.defineProperty(window, 'prism', {
    configurable: true,
    value: { management: { call: async (request: ManagementCall): Promise<ManagementReply> => {
      calls.push(request)
      if (request.method === 'PUT') {
        const body = request.body
        if (body === null || typeof body !== 'object' || !('pool' in body)) throw new Error('Missing pool')
        const pool = body.pool
        if (pool === null || typeof pool !== 'object' || !('pinnedAccount' in pool) || typeof pool.pinnedAccount !== 'string') throw new Error('Missing account selection')
        providers = [provider('codex', pool.pinnedAccount)]
        return { ok: true, status: 200, body: { generation: 2, provider: providers[0] } }
      }
      if (request.path === '/api/v1/providers') {
        return { ok: true, status: 200, body: { generation: 1, providers } }
      }
      if (request.path === '/api/v1/usage') {
        return { ok: true, status: 200, body: { accounts } }
      }
      throw new Error(`Unexpected request ${request.path}`)
    } } },
  })
})

afterEach(() => {
  act(() => root.unmount())
  element.remove()
  Reflect.deleteProperty(window, 'prism')
  vi.useRealTimers()
})

async function mount(): Promise<void> {
  await act(async () => { root.render(<UsagePanel />) })
}

it('hides providers without quota windows', async () => {
  accounts = [row('edge:default', 'edge'), row('codex:a')]
  providers = [provider('edge'), provider('codex')]
  await mount()
  expect(element.textContent).not.toContain('edge:default')
  expect(element.querySelector('.usage-card')?.textContent).toContain('codex:a')
  expect(element.querySelector('.usage-card')?.textContent).toContain('In use')
})

it('requires selection with two accounts and persists only the chosen account', async () => {
  accounts = [row('codex:a'), row('codex:b')]
  await mount()
  expect(element.textContent).toContain('codex has multiple accounts. Choose one')
  const button = Array.from(element.querySelectorAll('button')).find((candidate) => candidate.textContent?.includes('Use this account'))
  expect(button).toBeDefined()
  await act(async () => { button?.click() })
  expect(calls.find((call) => call.method === 'PUT')?.body).toMatchObject({ pool: { pinnedAccount: 'codex:a' } })
  expect(Array.from(element.querySelectorAll('.usage-card')).filter((card) => card.textContent?.includes('In use'))).toHaveLength(1)
})

it('shows a deleted selected account and allows choosing another', async () => {
  accounts = [row('codex:b')]
  providers = [provider('codex', 'codex:a')]
  await mount()
  expect(element.textContent).toContain('Selected account codex:a for codex is missing')
  const button = Array.from(element.querySelectorAll('button')).find((candidate) => candidate.textContent?.includes('Use this account'))
  await act(async () => { button?.click() })
  expect(calls.find((call) => call.method === 'PUT')?.body).toMatchObject({ pool: { pinnedAccount: 'codex:b' } })
  expect(element.textContent).not.toContain('Selected account codex:a for codex is missing')
})
