// Builds the macOS window-material Node-API addon (native/window-material) that lets the
// translucent shell set an adjustable desktop blur. The build never fails the app: without
// clang the renderer's blur control reports "Unavailable" and the window keeps plain vibrancy.

import { spawnSync } from 'node:child_process'
import { mkdirSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const scriptDir = path.dirname(fileURLToPath(import.meta.url))
const sourcePath = path.join(scriptDir, '..', 'native', 'window-material', 'WindowMaterial.m')
const MACOS_DEPLOYMENT_TARGET = '12.3'

export function buildWindowMaterialAddon(outputPath) {
  if (process.platform !== 'darwin') return false
  mkdirSync(path.dirname(outputPath), { recursive: true })
  const clang = spawnSync(
    'xcrun',
    [
      'clang',
      '-x', 'objective-c',
      '-fobjc-arc',
      '-O2', '-Wall', '-Wextra', '-Werror',
      '-arch', 'arm64', '-arch', 'x86_64',
      `-mmacosx-version-min=${MACOS_DEPLOYMENT_TARGET}`,
      // Node-API symbols resolve against the host Electron binary at load time.
      '-bundle', '-undefined', 'dynamic_lookup',
      '-framework', 'AppKit',
      sourcePath,
      '-o', outputPath,
    ],
    { encoding: 'utf8' },
  )
  if (clang.status !== 0) {
    const details = [clang.stdout, clang.stderr, clang.error?.message].filter(Boolean).join('\n').trim()
    console.warn(`prism: window-material addon was not built; the app falls back to vibrancy.\n${details}`)
    return false
  }
  const sign = spawnSync('codesign', ['--force', '--sign', '-', '--timestamp=none', outputPath], { encoding: 'utf8' })
  if (sign.status !== 0) {
    console.warn(`prism: could not ad-hoc sign the window-material addon: ${sign.stderr.trim()}`)
  }
  return true
}
