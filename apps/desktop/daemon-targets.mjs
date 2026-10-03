export const daemonTargets = [
  { GOOS: 'darwin', GOARCH: 'arm64', name: 'prism-darwin-arm64' },
  { GOOS: 'darwin', GOARCH: 'amd64', name: 'prism-darwin-amd64' },
  { GOOS: 'linux', GOARCH: 'amd64', name: 'prism-linux-amd64' },
  { GOOS: 'linux', GOARCH: 'arm64', name: 'prism-linux-arm64' },
  { GOOS: 'windows', GOARCH: 'amd64', name: 'prism-windows-amd64.exe' },
  { GOOS: 'windows', GOARCH: 'arm64', name: 'prism-windows-arm64.exe' },
]

export function selectDaemonTargets(targets, raw) {
  if (!raw) return targets
  const requested = raw.split(',').map(name => name.trim()).filter(Boolean)
  const requestedSet = new Set(requested)
  const selected = targets.filter(target => requestedSet.has(target.name))
  const known = new Set(targets.map(target => target.name))
  const unknown = requested.filter(name => !known.has(name))
  if (unknown.length > 0 || requested.length !== requestedSet.size || selected.length !== requestedSet.size) {
    throw new Error(`unknown PRISM_DAEMON_TARGETS: ${unknown.join(', ')}`)
  }
  return selected
}
