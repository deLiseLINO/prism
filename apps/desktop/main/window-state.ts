import { renameSync, writeFileSync } from 'node:fs'
import path from 'node:path'

export interface WindowState {
  readonly x: number
  readonly y: number
  readonly width: number
  readonly height: number
  readonly maximized: boolean
  readonly zoomFactor: number
}

interface Rectangle {
  readonly x: number
  readonly y: number
  readonly width: number
  readonly height: number
}

const MIN_WIDTH = 720
const MIN_HEIGHT = 480
const MIN_ZOOM_FACTOR = 0.4
const MAX_ZOOM_FACTOR = 3
const SAVE_DEBOUNCE_MS = 500

export function parseWindowState(value: unknown): WindowState | null {
  if (typeof value !== 'object' || value === null) return null
  const candidate = value as Partial<WindowState>
  const { x, y, width, height, maximized, zoomFactor } = candidate
  if (!isFiniteNumber(x) || !isFiniteNumber(y) || !isFiniteNumber(width) || !isFiniteNumber(height)) return null
  if (typeof maximized !== 'boolean' || !isFiniteNumber(zoomFactor)) return null
  if (width < MIN_WIDTH || height < MIN_HEIGHT) return null
  if (zoomFactor < MIN_ZOOM_FACTOR || zoomFactor > MAX_ZOOM_FACTOR) return null
  return { x, y, width, height, maximized, zoomFactor }
}

export function boundsOverlapArea(a: Rectangle, b: Rectangle): number {
  const left = Math.max(a.x, b.x)
  const top = Math.max(a.y, b.y)
  const right = Math.min(a.x + a.width, b.x + b.width)
  const bottom = Math.min(a.y + a.height, b.y + b.height)
  if (right <= left || bottom <= top) return 0
  return (right - left) * (bottom - top)
}

export function stateFilePath(userDataDir: string): string {
  return path.join(userDataDir, 'window-state.json')
}

export function saveWindowState(userDataDir: string, state: WindowState): void {
  const file = stateFilePath(userDataDir)
  const tmp = `${file}.tmp`
  writeFileSync(tmp, `${JSON.stringify(state)}\n`)
  renameSync(tmp, file)
}

export const WINDOW_SAVE_DEBOUNCE_MS = SAVE_DEBOUNCE_MS

function isFiniteNumber(value: unknown): value is number {
  return typeof value === 'number' && Number.isFinite(value)
}
