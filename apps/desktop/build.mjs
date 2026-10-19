import { build } from 'esbuild'
import { cp, copyFile, mkdir, readFile, rm } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { execFileSync } from 'node:child_process'
import { buildWindowMaterialAddon } from './scripts/build-window-material-addon.mjs'

// node's spawnSync does not resolve go.exe through PATHEXT on win32 in all
// environments (observed on GitHub's windows runners inside npm lifecycle
// scripts), so the executable name carries the extension there.
const goCommand = process.platform === 'win32' ? 'go.exe' : 'go'

const pkg = JSON.parse(await readFile(new URL('./package.json', import.meta.url), 'utf8'))
// RC/beta tags stamp their full tag version (0.1.0-rc4) into artifact names,
// embedded versions, and updater identity, so an rc can never be
// byte-different-but-name-identical to the final release of the same base.
// Unset (local dev, plain CI) keeps the package.json version. `--dev` appends a
// per-build suffix so `service start` replaces a running daemon of the previous
// build instead of reusing it (it compares versions only).
const releaseTag = process.env.PRISM_RELEASE_TAG
const baseVersion = releaseTag ? releaseTag.replace(/^v/, '') : pkg.version
const releaseVersion = process.argv.includes('--dev') ? `${baseVersion}-dev.${Math.floor(Date.now() / 1000)}` : baseVersion
// fileURLToPath handles win32 drive letters and percent-encoding; a raw URL
// pathname is a POSIX-ified path that breaks as a cwd on windows.
const scriptDir = fileURLToPath(new URL('.', import.meta.url))
// Everything resolves against the script, never process.cwd(): the script
// must behave identically from any cwd (npm workspace scripts pin it, but
// `node apps/desktop/build.mjs` from the repo root must not silently write
// to the wrong tree).
const prismDir = path.join(scriptDir, 'resources', 'prism')
const distDir = path.join(scriptDir, 'dist')
const versionFlag = `-X github.com/deLiseLINO/prism/internal/buildinfo.Version=${releaseVersion}`
const buildCwd = scriptDir
// Bundled daemon matrix: every supported platform/arch gets its own binary so
// the packaged app works on any build host (e.g. an arm64 mac producing an
// x64 dmg). The main process picks the right one via process.platform/arch
// (see main/daemon/locate.ts, main/hosts/install.ts, and daemon-targets.mjs).
const { daemonTargets, selectDaemonTargets } = await import('./daemon-targets.mjs')
const selectedDaemons = selectDaemonTargets(daemonTargets, process.env.PRISM_DAEMON_TARGETS)
// Stale binaries from a previous daemonTargets matrix must not ship.
await rm(prismDir, { recursive: true, force: true })
await rm(path.join(scriptDir, 'resources', 'webui'), { recursive: true, force: true })
await rm(distDir, { recursive: true, force: true })
await mkdir(prismDir, { recursive: true })
for (const { GOOS, GOARCH, name } of selectedDaemons) {
  const started = Date.now()
  execFileSync(goCommand, ['build', '-trimpath', '-ldflags', versionFlag, '-o', path.join(prismDir, name), '../../cmd/prism'], {
    cwd: buildCwd,
    stdio: 'inherit',
    env: { ...process.env, CGO_ENABLED: '0', GOOS, GOARCH },
  })
  console.log(`prism ${name} ${((Date.now() - started) / 1000).toFixed(1)}s`)
}


await mkdir(path.join(distDir, 'renderer'), { recursive: true })
await mkdir(path.join(distDir, 'webui'), { recursive: true })
await Promise.all([
  build({
    entryPoints: [path.join(scriptDir, 'main/index.ts')],
    bundle: true,
    platform: 'node',
    format: 'cjs',
    target: 'node20',
    external: ['electron', 'electron-updater'],
    outfile: path.join(distDir, 'main', 'index.js'),
  }),
  build({
    entryPoints: [path.join(scriptDir, 'preload/index.ts')],
    bundle: true,
    platform: 'node',
    format: 'cjs',
    target: 'node20',
    external: ['electron'],
    outfile: path.join(distDir, 'preload', 'index.js'),
  }),
  build({
    entryPoints: [path.join(scriptDir, 'renderer/src/index.tsx')],
    bundle: true,
    platform: 'browser',
    format: 'esm',
    target: 'es2022',
    jsx: 'automatic',
    outfile: path.join(distDir, 'renderer', 'index.js'),
  }),
  build({
    entryPoints: [path.join(scriptDir, 'renderer/src/index.tsx')],
    bundle: true,
    platform: 'browser',
    format: 'esm',
    target: 'es2022',
    jsx: 'automatic',
    outfile: path.join(distDir, 'webui', 'index.js'),
  }),
  copyFile(path.join(scriptDir, 'renderer/index.html'), path.join(distDir, 'renderer/index.html')),
  copyFile(path.join(scriptDir, 'renderer/styles.css'), path.join(distDir, 'renderer/styles.css')),
  copyFile(path.join(scriptDir, 'renderer/theme-boot.js'), path.join(distDir, 'renderer/theme-boot.js')),
  copyFile(path.join(scriptDir, 'renderer/web.html'), path.join(distDir, 'webui/index.html')),
  copyFile(path.join(scriptDir, 'renderer/styles.css'), path.join(distDir, 'webui/styles.css')),
  copyFile(path.join(scriptDir, 'renderer/theme-boot.js'), path.join(distDir, 'webui/theme-boot.js')),
])

if (process.platform === 'darwin') {
  buildWindowMaterialAddon(path.join(distDir, 'native', 'window-material.node'))
}

await cp(path.join(distDir, 'webui'), path.join(scriptDir, 'resources', 'webui'), { recursive: true })
