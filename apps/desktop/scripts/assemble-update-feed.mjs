import { copyFile, mkdir, readFile, readdir, writeFile } from 'node:fs/promises'
import path from 'node:path'
import { parse, stringify } from 'yaml'

const [source, destination, version] = process.argv.slice(2)
if (!source || !destination || !version) {
  throw new Error('usage: node assemble-update-feed.mjs SOURCE DESTINATION VERSION')
}

const channels = ['latest.yml', 'latest-mac.yml', 'latest-linux.yml', 'latest-linux-arm64.yml']
const names = new Set(await readdir(source))
for (const name of channels) {
  if (!names.has(name)) throw new Error(`missing channel ${name}`)
}
await mkdir(destination, { recursive: true })
for (const name of channels) {
  const info = parse(await readFile(path.join(source, name), 'utf8'))
  if (info.version !== version || !Array.isArray(info.files)) throw new Error(`invalid channel ${name}`)
  for (const file of info.files) {
    if (!names.has(file.url)) throw new Error(`${name} references missing ${file.url}`)
  }
  await copyFile(path.join(source, name), path.join(destination, name))
}
for (const name of names) {
  if (/\.(?:exe|dmg|zip|AppImage|blockmap)$/.test(name)) {
    await copyFile(path.join(source, name), path.join(destination, name))
  }
}
const macInfo = parse(await readFile(path.join(source, 'latest-mac.yml'), 'utf8'))
const zipFiles = macInfo.files.filter(file => file.url.endsWith('.zip'))
if (zipFiles.length !== 2) throw new Error('macOS update channel requires both ZIP architectures')
macInfo.files = zipFiles
macInfo.path = zipFiles[0].url
macInfo.sha512 = zipFiles[0].sha512
await writeFile(path.join(destination, 'latest-mac.yml'), stringify(macInfo))

for (const channel of channels) {
  const beta = channel.replace(/^latest/, 'beta')
  await copyFile(path.join(destination, channel), path.join(destination, beta))
}
