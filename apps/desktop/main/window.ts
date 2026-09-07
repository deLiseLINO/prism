import { app, BrowserWindow } from 'electron'
import path from 'node:path'
import { PRISM_HEADLESS_ENV, PRISM_RENDERER_URL_ENV } from './config'
import { installExternalNavigationGuard } from './window/navigation'

function isHeadless(): boolean {
  const raw = process.env[PRISM_HEADLESS_ENV] ?? ''
  return raw === '1' || raw.toLowerCase() === 'true'
}

export function createMainWindow(): BrowserWindow {
  const window = new BrowserWindow({
    width: 1200,
    height: 800,
    title: 'Prism',
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
  if (!isHeadless()) window.once('ready-to-show', () => window.show())
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
