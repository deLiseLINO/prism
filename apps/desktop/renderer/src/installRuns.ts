import { useSyncExternalStore } from 'react'
import type { HostId, HostInstallReply } from '@prism/contracts'
import { api } from './api'
import { describeError } from './useAsync'

const POLL_MS = 1_000

export type InstallPhase = 'probe' | 'upload' | 'start' | 'verify' | 'register'

export interface InstallRun {
  readonly host: HostId
  readonly phase: InstallPhase | null
  readonly error: string | null
  // Advice from the last successful install, e.g. prism will not start after a reboot.
  readonly notice: string | null
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

const EMPTY_RUN: InstallRun = { host: '' as HostId, phase: null, error: null, notice: null, done: false }

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
  runs.set(host, { host, phase: 'probe', error: null, notice: null, done: false })
  emit()
  let reply: HostInstallReply | null
  try {
    let status = await api.localInstallHost(host)
    while (status.running) {
      await new Promise((resolve) => setTimeout(resolve, POLL_MS))
      status = await api.localHostInstall(host)
    }
    reply = status.reply
  } catch (error) {
    reply = { ok: false, host, phase: 'probe', reason: `the install request failed: ${describeError(error instanceof Error ? error : new Error(String(error)))}` }
  }
  let ok = false
  if (reply?.ok === true) {
    runs.set(host, { host, phase: null, error: null, notice: reply.notice ?? null, done: true })
    ok = true
  } else {
    runs.set(host, { host, phase: null, error: reply?.reason ?? 'the install finished without a result', notice: null, done: false })
  }
  emit()
  for (const listener of settledListeners) listener(host, ok)
}
