import { build } from 'esbuild'
import { cp, copyFile, mkdir, readFile, rm } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { execFileSync } from 'node:child_process'

// node's spawnSync does not resolve go.exe through PATHEXT on win32 in all
// environments (observed on GitHub's windows runners inside npm lifecycle
// scripts), so the executable name carries the extension there.
const goCommand = process.platform === 'win32' ? 'go.exe' : 'go'

const pkg = JSON.parse(await readFile(new URL('./package.json', import.meta.url), 'utf8'))
// fileURLToPath handles win32 drive letters and percent-encoding; a raw URL
// pathname is a POSIX-ified path that breaks as a cwd on windows.
const scriptDir = fileURLToPath(new URL('.', import.meta.url))
// Everything resolves against the script, never process.cwd(): the script
// must behave identically from any cwd (npm workspace scripts pin it, but
// `node apps/desktop/build.mjs` from the repo root must not silently write
// to the wrong tree).
const prismdDir = path.join(scriptDir, 'resources', 'prismd')
const distDir = path.join(scriptDir, 'dist')
const versionFlag = `-X prism/internal/buildinfo.Version=${pkg.version}`
const buildCwd = scriptDir
// Bundled daemon matrix: every supported platform/arch gets its own binary so
// the packaged app works on any build host (e.g. an arm64 mac producing an
// x64 dmg). The main process picks the right one via process.platform/arch
// (see main/daemon/locate.ts and main/hosts/install.ts).
const daemonTargets = [
  { GOOS: 'darwin', GOARCH: 'arm64', name: 'prismd-darwin-arm64' },
  { GOOS: 'darwin', GOARCH: 'amd64', name: 'prismd-darwin-amd64' },
  { GOOS: 'linux', GOARCH: 'amd64', name: 'prismd-linux-amd64' },
  { GOOS: 'linux', GOARCH: 'arm64', name: 'prismd-linux-arm64' },
  { GOOS: 'windows', GOARCH: 'amd64', name: 'prismd-windows-amd64.exe' },
  { GOOS: 'windows', GOARCH: 'arm64', name: 'prismd-windows-arm64.exe' },
]
// Stale binaries from a previous daemonTargets matrix must not ship:
// resources/prismd is gitignored and never cleaned by anything else.
// The full matrix ships in every installer (the extraResources copy is not
// arch-filtered); the main process picks its binary at runtime.
await rm(prismdDir, { recursive: true, force: true })
await rm(path.join(scriptDir, 'resources', 'webui'), { recursive: true, force: true })
await rm(distDir, { recursive: true, force: true })
await mkdir(prismdDir, { recursive: true })
for (const { GOOS, GOARCH, name } of daemonTargets) {
  execFileSync(goCommand, ['build', '-trimpath', '-ldflags', versionFlag, '-o', path.join(prismdDir, name), '../../cmd/prismd'], {
    cwd: buildCwd,
    stdio: 'inherit',
    env: { ...process.env, CGO_ENABLED: '0', GOOS, GOARCH },
  })
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

await cp(path.join(distDir, 'webui'), path.join(scriptDir, 'resources', 'webui'), { recursive: true })
