import { build } from 'esbuild'
import { copyFile, mkdir } from 'node:fs/promises'
import path from 'node:path'
import { execFileSync } from 'node:child_process'


const prismdBinary = path.join('resources', 'prismd', 'prismd')
execFileSync('go', ['build', '-o', prismdBinary, '../../cmd/prismd'], {
  cwd: new URL('.', import.meta.url).pathname,
  stdio: 'inherit',
})

await mkdir(path.join('dist', 'renderer'), { recursive: true })

await Promise.all([
  build({
    entryPoints: ['main/index.ts'],
    bundle: true,
    platform: 'node',
    format: 'cjs',
    target: 'node20',
    external: ['electron'],
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
  copyFile('renderer/index.html', 'dist/renderer/index.html'),
  copyFile('renderer/styles.css', 'dist/renderer/styles.css'),
  copyFile('renderer/theme-boot.js', 'dist/renderer/theme-boot.js'),
])
