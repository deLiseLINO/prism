import { useSyncExternalStore } from 'react'
import type { HostId, HostInstallReply } from '@prism/contracts'

export type InstallPhase = 'probe' | 'upload' | 'start' | 'verify' | 'register'

export interface InstallRun {
  readonly host: HostId
  readonly phase: InstallPhase | null
  readonly error: string | null
  readonly done: boolean
}

const runs = new Map<HostId, InstallRun>()

const listeners = new Set<() => void>()
const settledListeners = new Set<(host: HostId, ok: boolean) => void>()

function emit(): void {
  for (const listener of listeners) listener()
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
  }
}

export function onInstallSettled(listener: (host: HostId, ok: boolean) => void): () => void {
  settledListeners.add(listener)
  return () => {
    settledListeners.delete(listener)
  }
}

const EMPTY_RUN: InstallRun = { host: '' as HostId, phase: null, error: null, done: false }

export function getInstallRun(host: HostId): InstallRun {
  return runs.get(host) ?? EMPTY_RUN
}

export function useInstallRun(host: HostId): InstallRun {
  return useSyncExternalStore(
    subscribe,
    () => runs.get(host) ?? EMPTY_RUN,
    () => EMPTY_RUN,
  )
}

export async function startInstall(host: HostId): Promise<void> {
  const current = runs.get(host)
  if (current !== undefined && current.phase !== null) return
  runs.set(host, { host, phase: 'probe', error: null, done: false })
  emit()
  const reply: HostInstallReply | undefined = await window.prism.integrations.installDaemon({ host })
  let ok = false
  if (reply !== undefined && reply.ok) {
    runs.set(host, { host, phase: null, error: null, done: true })
    ok = true
  } else if (reply !== undefined) {
    runs.set(host, { host, phase: null, error: reply.reason, done: false })
  } else {
    runs.set(host, { host, phase: null, error: 'the install request failed', done: false })
  }
  emit()
  for (const listener of settledListeners) listener(host, ok)
}
