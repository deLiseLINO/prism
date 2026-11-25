import type { ManagementProxy } from '../../shared/management'

// maintainHostDaemons asks the daemon to rerun the install on every machine
// that already has prism, so after this app updates each machine moves to the
// same version and a stopped prism without a service starts again. The daemon
// runs the installs in the background.
export async function maintainHostDaemons(management: ManagementProxy): Promise<void> {
  const reply = await management.call({ method: 'GET', path: '/api/v1/hosts' })
  if (!reply.ok || reply.status !== 200) return
  const body = reply.body as { hosts?: { id?: unknown; local?: unknown; daemonPort?: unknown }[] } | undefined
  const installed = (body?.hosts ?? []).filter((h) => h.local !== true && typeof h.id === 'string' && typeof h.daemonPort === 'number')
  await Promise.all(installed.map(async (h) => {
    const started = await management.call({ method: 'POST', path: `/api/v1/hosts/${encodeURIComponent(h.id as string)}/install` })
    if (started.status !== 202) console.error(`prism: keeping ${String(h.id)} up to date failed to start: HTTP ${started.status}`)
  }))
}
