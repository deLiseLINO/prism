import { app, BrowserWindow, screen } from 'electron'
import { readFileSync } from 'node:fs'
import path from 'node:path'
import { PRISM_HEADLESS_ENV, PRISM_RENDERER_URL_ENV } from './config'
import {
  WINDOW_SAVE_DEBOUNCE_MS,
  boundsOverlapArea,
  parseWindowState,
  saveWindowState,
  stateFilePath,
  type WindowState,
} from './window-state'
import { installExternalNavigationGuard } from './window/navigation'

const DEFAULT_WIDTH = 1200
const DEFAULT_HEIGHT = 800

function isHeadless(): boolean {
  const raw = process.env[PRISM_HEADLESS_ENV] ?? ''
  return raw === '1' || raw.toLowerCase() === 'true'
}

function loadWindowState(): WindowState | null {
  try {
    return parseWindowState(JSON.parse(readFileSync(stateFilePath(app.getPath('userData')), 'utf8')))
  } catch {
    return null
  }
}

interface RestoredBounds {
  readonly width: number
  readonly height: number
  readonly x?: number
  readonly y?: number
}

function restoredBounds(state: WindowState | null): RestoredBounds {
  if (state === null) return { width: DEFAULT_WIDTH, height: DEFAULT_HEIGHT }
  const bounds = { x: state.x, y: state.y, width: state.width, height: state.height }
  const workArea = screen.getDisplayMatching(bounds).workArea
  const visible = boundsOverlapArea(bounds, workArea)
  if (visible < 200 * 200) return { width: DEFAULT_WIDTH, height: DEFAULT_HEIGHT }
  return bounds
}

function trackWindowState(window: BrowserWindow, state: WindowState | null): void {
  if (isHeadless()) return
  const userDataDir = app.getPath('userData')
  let timer: NodeJS.Timeout | null = null

  const current = (): WindowState => {
    const bounds = window.isMaximized() ? window.getNormalBounds() : window.getBounds()
    return {
      x: Math.round(bounds.x),
      y: Math.round(bounds.y),
      width: Math.round(bounds.width),
      height: Math.round(bounds.height),
      maximized: window.isMaximized(),
      zoomFactor: window.webContents.zoomFactor,
    }
  }

  const save = (): void => {
    if (window.isDestroyed()) return
    saveWindowState(userDataDir, current())
  }

  const scheduleSave = (): void => {
    if (timer !== null) clearTimeout(timer)
    timer = setTimeout(save, WINDOW_SAVE_DEBOUNCE_MS)
  }

  window.on('resize', scheduleSave)
  window.on('move', scheduleSave)
  window.on('maximize', scheduleSave)
  window.on('unmaximize', scheduleSave)
  window.webContents.on('did-navigate', save)
  window.on('close', () => {
    if (timer !== null) clearTimeout(timer)
    save()
  })

  if (state !== null) window.webContents.zoomFactor = state.zoomFactor
}

export function createMainWindow(): BrowserWindow {
  const state = isHeadless() ? null : loadWindowState()
  const bounds = restoredBounds(state)
  const window = new BrowserWindow({
    ...bounds,
    title: 'Prism',
    minWidth: 820,
    minHeight: 560,
    show: false,
    backgroundColor: '#101014',
    webPreferences: {
      preload: path.join(__dirname, '..', 'preload', 'index.js'),
      contextIsolation: true,
      sandbox: true,
      nodeIntegration: false,
      nodeIntegrationInWorker: false,
      nodeIntegrationInSubFrames: false,
      allowRunningInsecureContent: false,
      experimentalFeatures: false,
      spellcheck: false,
      devTools: !app.isPackaged,
    },
  })
  if (state?.maximized === true) window.maximize()
  if (!isHeadless()) window.once('ready-to-show', () => window.show())
  trackWindowState(window, state)
  const devUrl = process.env[PRISM_RENDERER_URL_ENV]
  let currentOrigin = 'file://'
  if (devUrl !== undefined && devUrl !== '') {
    try {
      currentOrigin = new URL(devUrl).origin
    } catch {
      currentOrigin = devUrl
    }
    void window.loadURL(devUrl)
  } else {
    window.webContents.on('did-fail-load', (_event, code, description, url) => {
      console.error(`prism: renderer failed to load ${url}: ${description} (${code})`)
    })
    void window.loadFile(path.join(__dirname, '..', 'renderer', 'index.html'))
  }
  installExternalNavigationGuard(window, currentOrigin)
  return window
}
