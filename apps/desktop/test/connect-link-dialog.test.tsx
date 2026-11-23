// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { renderToString } from 'react-dom/server'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ConnectPlan } from '@prism/contracts'
import { ConnectLinkDialog, ConnectPlanBody } from '../renderer/src/components/ConnectLinkDialog'
import { api } from '../renderer/src/api'

vi.mock('../renderer/src/bridge', () => ({ bridge: {} }))

const PLAN: ConnectPlan = {
  id: 'edge',
  wire: 'chat',
  baseURL: 'http://10.0.0.1:8080/v1',
  fallbackBaseURLs: ['http://10.0.0.2:8080/v1', 'https://edge.example/v1'],
  endpointMode: 'failover',
  models: [],
  modelWires: [],
  defaultModel: '',
  keyPreview: 'sk-a…',
  hasKey: true,
  exists: false,
}

const FULL_KEY = 'sk-abcdef0123456789'
const LINK = `prism://connect?v=1&id=edge&wire=chat&endpoint=http%3A%2F%2F10.0.0.1%3A8080%2Fv1&key=${FULL_KEY}`

describe('connect link dialog body', () => {
  it('lists every endpoint with one primary badge', () => {
    const html = renderToString(<ConnectPlanBody plan={PLAN} />)
    expect(html).toContain('http://10.0.0.1:8080/v1')
    expect(html).toContain('http://10.0.0.2:8080/v1')
    expect(html).toContain('https://edge.example/v1')
    expect(html.match(/>primary</g)).toHaveLength(1)
    expect(html.indexOf('http://10.0.0.1:8080/v1')).toBeLessThan(html.indexOf('http://10.0.0.2:8080/v1'))
    expect(html).toContain('discovered after adding')
  })
})

describe('connect link dialog', () => {
  let root: Root
  let element: HTMLDivElement

  beforeEach(() => {
    Reflect.set(globalThis, 'IS_REACT_ACT_ENVIRONMENT', true)
    element = document.createElement('div')
    document.body.appendChild(element)
    root = createRoot(element)
    vi.spyOn(api, 'connect').mockImplementation(async () => ({ applied: false, generation: 1, plan: PLAN, provider: null, warning: '' }))
  })

  afterEach(() => {
    act(() => root.unmount())
    element.remove()
    vi.restoreAllMocks()
  })

  async function mount(host: string): Promise<void> {
    await act(async () => {
      root.render(<ConnectLinkDialog link={LINK} host={host} onCancel={() => {}} onAdded={() => {}} onBusyChange={() => {}} />)
    })
  }

  it('shows the key preview from the dry run and never the key in the link', async () => {
    await mount('local')
    expect(api.connect).toHaveBeenCalledWith('local', LINK, true)
    const text = document.body.textContent ?? ''
    expect(text).toContain('sk-a…')
    expect(text).not.toContain(FULL_KEY)
    expect(text).not.toContain('will be added to')
  })

  it('names the remote machine the provider goes to', async () => {
    await mount('build-box')
    expect(api.connect).toHaveBeenCalledWith('build-box', LINK, true)
    expect(document.body.textContent).toContain('The provider will be added to build-box.')
  })
})
