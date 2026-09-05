import { describe, expect, it } from 'vitest'
import { describeError } from '../renderer/src/useAsync'

describe('describeError helper', () => {
  it('passes through plain Error messages', () => {
    expect(describeError(new Error('hi'))).toBe('hi')
  })

  it('prefixes a stable code when the error carries one', () => {
    const err = Object.assign(new Error('moved on'), { code: 'stale_generation' })
    expect(describeError(err)).toBe('stale_generation: moved on')
  })

  it('ignores non-string codes', () => {
    const err = Object.assign(new Error('raw'), { code: 42 })
    expect(describeError(err)).toBe('raw')
  })

  it('handles errors without a code property', () => {
    const err = Object.assign(new Error('plain'), { foo: 'bar' })
    expect(describeError(err)).toBe('plain')
  })

  it('handles non-Error values safely', () => {
    expect(describeError(new TypeError('bad input'))).toBe('bad input')
  })
})