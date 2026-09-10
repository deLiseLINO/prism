import { build } from 'esbuild'
import { cp, copyFile, mkdir, readFile } from 'node:fs/promises'
import path from 'node:path'
import { execFileSync } from 'node:child_process'

const pkg = JSON.parse(await readFile(new URL('./package.json', import.meta.url), 'utf8'))
const prismdBinary = path.join('resources', 'prismd', 'prismd')
execFileSync(
  'go',
  [
    'build',
    '-ldflags',
    `-X prism/internal/buildinfo.Version=${pkg.version}`,
    '-o',
    prismdBinary,
    '../../cmd/prismd',
  ],
  {
    cwd: new URL('.', import.meta.url).pathname,
    stdio: 'inherit',
  },
)

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
