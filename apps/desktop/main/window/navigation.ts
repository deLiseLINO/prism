import { BrowserWindow, shell } from 'electron'

export interface ExternalNavigationPolicy {
  readonly denyNewWindows: boolean
  readonly denyCrossOriginNavigation: boolean
  readonly openExternalHttpHttps: boolean
}

export const DEFAULT_NAVIGATION_POLICY: ExternalNavigationPolicy = {
  denyNewWindows: true,
  denyCrossOriginNavigation: true,
  openExternalHttpHttps: true,
}

export type ExternalNavigationAction = 'deny' | 'external' | 'allow'

export interface ExternalNavigationVerdict {
  readonly action: ExternalNavigationAction
  readonly reason: string
}

export function evaluateExternalNavigation(
  url: string,
  currentOrigin: string,
  policy: ExternalNavigationPolicy,
): ExternalNavigationVerdict {
  let parsed: URL
  try {
    parsed = new URL(url)
  } catch {
    return { action: 'deny', reason: 'unparseable url' }
  }
  if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
    return { action: 'deny', reason: `non-http(s) protocol ${parsed.protocol}` }
  }
  if (policy.denyCrossOriginNavigation && parsed.origin !== currentOrigin) {
    if (policy.openExternalHttpHttps) {
      return { action: 'external', reason: 'cross-origin http(s) handed to shell.openExternal' }
    }
    return { action: 'deny', reason: 'cross-origin navigation denied by policy' }
  }
  return { action: 'allow', reason: 'same-origin http(s)' }
}

export function installExternalNavigationGuard(
  window: BrowserWindow,
  currentOrigin: string,
  policy: ExternalNavigationPolicy = DEFAULT_NAVIGATION_POLICY,
): void {
  const contents = window.webContents
  contents.setWindowOpenHandler(({ url }) => {
    const verdict = evaluateExternalNavigation(url, currentOrigin, policy)
    if (verdict.action === 'external') {
      void shell.openExternal(url)
    }
    return { action: 'deny' }
  })
  contents.on('will-navigate', (event, url) => {
    const verdict = evaluateExternalNavigation(url, currentOrigin, policy)
    if (verdict.action === 'allow') return
    event.preventDefault()
    if (verdict.action === 'external') {
      void shell.openExternal(url)
    }
  })
  contents.on('will-redirect', (event, url) => {
    const verdict = evaluateExternalNavigation(url, currentOrigin, policy)
    if (verdict.action === 'allow') return
    event.preventDefault()
    if (verdict.action === 'external') {
      void shell.openExternal(url)
    }
  })
}
