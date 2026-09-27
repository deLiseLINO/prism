import { copyFile, mkdir, readFile, readdir, unlink, writeFile } from 'node:fs/promises'
import path from 'node:path'
import { parse, stringify } from 'yaml'

const [source, destination, version] = process.argv.slice(2)
if (!source || !destination || !version) {
  throw new Error('usage: node assemble-update-feed.mjs SOURCE DESTINATION VERSION')
}

const channels = ['latest.yml', 'latest-mac.yml', 'latest-linux.yml', 'latest-linux-arm64.yml']
const names = new Set(await readdir(source))
const macChannels = [...names].filter(name => /^latest-mac(?:-.+)?\.yml$/.test(name)).sort()
if (macChannels.length === 0) throw new Error('missing channel latest-mac.yml')
for (const name of channels.filter(name => name !== 'latest-mac.yml')) {
  if (!names.has(name)) throw new Error(`missing channel ${name}`)
}
await mkdir(destination, { recursive: true })

const macParts = []
for (const name of macChannels) {
  const info = parse(await readFile(path.join(source, name), 'utf8'))
  if (info.version !== version || !Array.isArray(info.files)) throw new Error(`invalid channel ${name}`)
  macParts.push(info)
}
const macInfo = { ...macParts[0] }
const seen = new Set()
macInfo.files = macParts.flatMap(info => info.files).filter(file => {
  if (seen.has(file.url)) return false
  seen.add(file.url)
  return true
})
for (const file of macInfo.files) {
  if (!names.has(file.url)) throw new Error(`latest-mac.yml references missing ${file.url}`)
}
const macZipFiles = macInfo.files.filter(file => file.url.endsWith('.zip'))
if (macZipFiles.length !== 2) throw new Error('macOS update channel requires both ZIP architectures')
macInfo.files = macZipFiles
macInfo.path = macZipFiles[0].url
macInfo.sha512 = macZipFiles[0].sha512

for (const name of channels.filter(name => name !== 'latest-mac.yml')) {
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
await writeFile(path.join(destination, 'latest-mac.yml'), stringify(macInfo))

const channel = version.match(/-(beta|rc)\.[0-9]+$/)?.[1] ?? 'latest'
if (channel !== 'latest') {
  for (const name of channels) {
    await copyFile(path.join(destination, name), path.join(destination, name.replace(/^latest/, channel)))
    await unlink(path.join(destination, name))
  }
}
