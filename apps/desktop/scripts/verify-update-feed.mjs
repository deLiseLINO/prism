import { createHash } from 'node:crypto'
import { createReadStream } from 'node:fs'
import { readdir, stat, readFile } from 'node:fs/promises'
import path from 'node:path'
import { parse } from 'yaml'

const [directory, version, mode] = process.argv.slice(2)
if (!directory || !version || (mode !== undefined && mode !== '--complete')) {
  throw new Error('usage: node verify-update-feed.mjs DIRECTORY VERSION [--complete]')
}

const stableChannels = ['latest.yml', 'latest-mac.yml', 'latest-linux.yml', 'latest-linux-arm64.yml']
const channels = [...stableChannels, ...['beta', 'rc'].flatMap(prefix => stableChannels.map(name => name.replace(/^latest/, prefix)))]
const names = await readdir(directory)
if (mode === '--complete') {
  for (const channel of channels) {
    if (!names.includes(channel)) throw new Error(`missing update channel ${channel}`)
  }
}

const expected = {
  'latest.yml': [`Prism-${version}-setup.exe`],
  'latest-mac.yml': ['arm64', 'x64'].map(arch => `Prism-${version}-${arch}.zip`),
  'latest-linux.yml': [`Prism-${version}-x86_64.AppImage`],
  'latest-linux-arm64.yml': [`Prism-${version}-arm64.AppImage`],
}

let checked = 0
for (const channel of channels.filter(name => names.includes(name))) {
  const info = parse(await readFile(path.join(directory, channel), 'utf8'))
  if (info?.version !== version || !Array.isArray(info.files)) {
    throw new Error(`${channel}: invalid version or files`)
  }
  const actual = info.files.map(file => file.url)
  const wanted = expected[channel.replace(/^(beta|rc)/, 'latest')]
  if (actual.length === 0 || actual.some(name => !wanted.includes(name)) ||
      new Set(actual).size !== actual.length ||
      (mode === '--complete' && (actual.length !== wanted.length || wanted.some(name => !actual.includes(name))))) {
    throw new Error(`${channel}: expected ${wanted.join(', ')}, got ${actual.join(', ')}`)
  }
  if (info.path !== undefined && !actual.includes(info.path)) {
    throw new Error(`${channel}: path does not reference an update artifact`)
  }
  if (info.sha512 !== undefined && !info.files.some(file => file.sha512 === info.sha512)) {
    throw new Error(`${channel}: top-level checksum does not match an update artifact`)
  }
  for (const file of info.files) {
    const filename = path.join(directory, file.url)
    const size = (await stat(filename)).size
    if (size !== file.size) throw new Error(`${channel}: size mismatch for ${file.url}`)
    const hash = createHash('sha512')
    for await (const chunk of createReadStream(filename)) hash.update(chunk)
    if (hash.digest('base64') !== file.sha512) throw new Error(`${channel}: checksum mismatch for ${file.url}`)
    checked++
  }
}
if (checked === 0) throw new Error('no update channels found')
console.info(`Verified ${checked} update artifacts across ${channels.filter(name => names.includes(name)).length} channels`)
