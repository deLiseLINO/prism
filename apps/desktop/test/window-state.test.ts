import { describe, expect, it } from 'vitest'
import { existsSync, mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { boundsOverlapArea, parseWindowState, saveWindowState } from '../main/window-state'

describe('parseWindowState', () => {
  it('accepts a valid state', () => {
    expect(parseWindowState({ x: 10, y: 20, width: 1200, height: 800, maximized: false, zoomFactor: 1.25 })).toEqual({
      x: 10,
      y: 20,
      width: 1200,
      height: 800,
      maximized: false,
      zoomFactor: 1.25,
    })
  })

  it('rejects invalid shapes and ranges', () => {
    for (const value of [
      null,
      'x',
      {},
      { x: 'a', y: 20, width: 1200, height: 800, maximized: false, zoomFactor: 1 },
      { x: 10, y: 20, width: 100, height: 800, maximized: false, zoomFactor: 1 },
      { x: 10, y: 20, width: 1200, height: 800, maximized: 'yes', zoomFactor: 1 },
      { x: 10, y: 20, width: 1200, height: 800, maximized: false, zoomFactor: 0.1 },
      { x: 10, y: 20, width: 1200, height: 800, maximized: false, zoomFactor: Number.POSITIVE_INFINITY },
    ]) {
      expect(parseWindowState(value)).toBeNull()
    }
  })
})

describe('saveWindowState', () => {
  it('persists state to the target file, not just the tmp sidecar', () => {
    const dir = mkdtempSync(join(tmpdir(), 'prism-window-state-'))
    try {
      const state = { x: 10, y: 20, width: 1200, height: 800, maximized: false, zoomFactor: 1.25 }
      saveWindowState(dir, state)
      expect(parseWindowState(JSON.parse(readFileSync(join(dir, 'window-state.json'), 'utf8')))).toEqual(state)
      expect(existsSync(join(dir, 'window-state.json.tmp'))).toBe(false)
    } finally {
      rmSync(dir, { recursive: true, force: true })
    }
  })
})

describe('boundsOverlapArea', () => {
  it('computes overlap and returns zero for disjoint rectangles', () => {
    expect(boundsOverlapArea({ x: 0, y: 0, width: 100, height: 100 }, { x: 50, y: 50, width: 100, height: 100 })).toBe(2500)
    expect(boundsOverlapArea({ x: 0, y: 0, width: 10, height: 10 }, { x: 20, y: 20, width: 10, height: 10 })).toBe(0)
  })
})
