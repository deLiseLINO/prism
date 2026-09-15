import { build } from 'esbuild'
import { cp, copyFile, mkdir, readFile } from 'node:fs/promises'
import path from 'node:path'
import { execFileSync } from 'node:child_process'

const pkg = JSON.parse(await readFile(new URL('./package.json', import.meta.url), 'utf8'))
const prismdDir = path.join('resources', 'prismd')
const versionFlag = `-X prism/internal/buildinfo.Version=${pkg.version}`
const buildCwd = new URL('.', import.meta.url).pathname
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
for (const { GOOS, GOARCH, name } of daemonTargets) {
  execFileSync('go', ['build', '-trimpath', '-ldflags', versionFlag, '-o', path.join(prismdDir, name), '../../cmd/prismd'], {
    cwd: buildCwd,
    stdio: 'inherit',
    env: { ...process.env, CGO_ENABLED: '0', GOOS, GOARCH },
  })
}


await mkdir(path.join('dist', 'renderer'), { recursive: true })
await mkdir(path.join('dist', 'webui'), { recursive: true })
await Promise.all([
  build({
    entryPoints: ['main/index.ts'],
    bundle: true,
    platform: 'node',
    format: 'cjs',
    target: 'node20',
    external: ['electron', 'electron-updater'],
    outfile: 'dist/main/index.js',
  }),
  build({
    entryPoints: ['preload/index.ts'],
    bundle: true,
    platform: 'node',
    format: 'cjs',
    target: 'node20',
    external: ['electron'],
    outfile: 'dist/preload/index.js',
  }),
  build({
    entryPoints: ['renderer/src/index.tsx'],
    bundle: true,
    platform: 'browser',
    format: 'esm',
    target: 'es2022',
    jsx: 'automatic',
    outfile: 'dist/renderer/index.js',
  }),
  build({
    entryPoints: ['renderer/src/index.tsx'],
    bundle: true,
    platform: 'browser',
    format: 'esm',
    target: 'es2022',
    jsx: 'automatic',
    outfile: 'dist/webui/index.js',
  }),
  copyFile('renderer/index.html', 'dist/renderer/index.html'),
  copyFile('renderer/styles.css', 'dist/renderer/styles.css'),
  copyFile('renderer/theme-boot.js', 'dist/renderer/theme-boot.js'),
  copyFile('renderer/web.html', 'dist/webui/index.html'),
  copyFile('renderer/styles.css', 'dist/webui/styles.css'),
  copyFile('renderer/theme-boot.js', 'dist/webui/theme-boot.js'),
])

await cp(path.join('dist', 'webui'), path.join('resources', 'webui'), { recursive: true })
