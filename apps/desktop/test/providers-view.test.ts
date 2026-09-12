import { describe, expect, it } from 'vitest'
import { sortProviders } from '../renderer/src/views/ProvidersView'
import type { ProviderView } from '@prism/contracts'

function provider(id: string, enabled: boolean): ProviderView {
  return {
    id,
    enabled,
    models: [],
    disabledModels: [],
    credential: { state: 'unset' },
    wire: 'openai',
  } as unknown as ProviderView
}

describe('provider rail sort', () => {
  it('moves disabled providers after enabled ones using the snapshot', () => {
    const list = [provider('a', false), provider('b', true), provider('c', false)]
    const sorted = sortProviders(list, { a: false, b: false, c: true })
    expect(sorted.map((p) => p.id)).toEqual(['a', 'b', 'c'])
  })

  it('keeps current order when a toggle is not in the snapshot', () => {
    const list = [provider('a', true), provider('b', false)]
    const sorted = sortProviders(list, { a: false, b: false })
    expect(sorted.map((p) => p.id)).toEqual(['a', 'b'])
  })

  it('does not mutate the input list', () => {
    const list = [provider('a', false), provider('b', true)]
    sortProviders(list, { a: true, b: false })
    expect(list.map((p) => p.id)).toEqual(['a', 'b'])
  })

  it('treats missing snapshot entries as enabled', () => {
    const list = [provider('a', false), provider('b', true)]
    const sorted = sortProviders(list, {})
    expect(sorted.map((p) => p.id)).toEqual(['a', 'b'])
  })
})
