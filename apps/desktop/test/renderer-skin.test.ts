import { describe, expect, it } from 'vitest'
import { isSkin, nextSkin } from '../renderer/src/useSkin'

describe('isSkin', () => {
  it('accepts both skin names and rejects everything else', () => {
    expect(isSkin('obsidian')).toBe(true)
    expect(isSkin('graphite')).toBe(true)
    expect(isSkin('aurora')).toBe(false)
    expect(isSkin(null)).toBe(false)
  })
})

describe('nextSkin', () => {
  it('switches between the two skins in both directions', () => {
    expect(nextSkin('obsidian')).toBe('graphite')
    expect(nextSkin('graphite')).toBe('obsidian')
  })
})
