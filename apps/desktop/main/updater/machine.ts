import type { UpdaterErrorStage, UpdaterState, UpdaterStatus } from '@prism/contracts'

export type UpdateEvent =
  | { type: 'disable' }
  | { type: 'check-request' }
  | { type: 'check-due' }
  | { type: 'check-started' }
  | { type: 'update-available'; version: string }
  | { type: 'no-update' }
  | { type: 'check-failure'; error: string }
  | { type: 'download-started' }
  | { type: 'download-progress'; percent: number }
  | { type: 'download-complete'; version: string }
  | { type: 'download-failure'; error: string }
  | { type: 'install-request' }
  | { type: 'install-failure'; error: string }

export type UpdateEffect =
  | { kind: 'none' }
  | { kind: 'begin-check' }
  | { kind: 'schedule-check'; delayMs: number }
  | { kind: 'quit-for-install' }

export interface UpdatePolicy {
  readonly initialCheckDelayMs: number
  readonly pollIntervalMs: number
}

export const DEFAULT_UPDATE_POLICY: UpdatePolicy = {
  initialCheckDelayMs: 15_000,
  pollIntervalMs: 240_000,
}

export interface UpdaterModel {
  readonly state: UpdaterState
  readonly currentVersion: string
  readonly availableVersion: string | null
  readonly downloadedVersion: string | null
  readonly progress: number | null
  readonly error: string | null
  readonly errorStage: UpdaterErrorStage | null
  readonly lastCheckedAt: string | null
}

export function initialModel(currentVersion: string): UpdaterModel {
  return {
    state: 'idle',
    currentVersion,
    availableVersion: null,
    downloadedVersion: null,
    progress: null,
    error: null,
    errorStage: null,
    lastCheckedAt: null,
  }
}

export function statusOf(model: UpdaterModel): UpdaterStatus {
  return {
    state: model.state,
    currentVersion: model.currentVersion,
    availableVersion: model.availableVersion,
    downloadedVersion: model.downloadedVersion,
    progress: model.progress,
    error: model.error,
    errorStage: model.errorStage,
    canRetry: model.state === 'error',
    lastCheckedAt: model.lastCheckedAt,
  }
}

const NO_EFFECT = { kind: 'none' } as const

export function step(
  model: UpdaterModel,
  event: UpdateEvent,
  policy: UpdatePolicy,
  now: () => string = () => new Date().toISOString(),
): { model: UpdaterModel; effect: UpdateEffect } {
  switch (event.type) {
    case 'disable':
      return { model: { ...model, state: 'disabled' }, effect: NO_EFFECT }
    case 'check-request':
    case 'check-due': {
      if (!canCheck(model.state)) return { model, effect: NO_EFFECT }
      return { model: { ...model, state: 'checking', progress: null }, effect: { kind: 'begin-check' } }
    }
    case 'check-started': {
      if (model.state !== 'checking') return { model, effect: NO_EFFECT }
      return { model: { ...model, error: null, errorStage: null }, effect: NO_EFFECT }
    }
    case 'update-available': {
      if (model.state !== 'checking') return { model, effect: NO_EFFECT }
      return {
        model: {
          ...model,
          state: 'available',
          availableVersion: event.version,
          error: null,
          errorStage: null,
          lastCheckedAt: now(),
        },
        effect: NO_EFFECT,
      }
    }
    case 'no-update': {
      if (model.state !== 'checking') return { model, effect: NO_EFFECT }
      const keep = model.downloadedVersion !== null
      return {
        model: {
          ...model,
          state: keep ? 'downloaded' : 'up-to-date',
          availableVersion: keep ? model.downloadedVersion : null,
          progress: keep ? 100 : null,
          error: null,
          errorStage: null,
          lastCheckedAt: now(),
        },
        effect: { kind: 'schedule-check', delayMs: policy.pollIntervalMs },
      }
    }
    case 'check-failure': {
      if (model.state !== 'checking') return { model, effect: NO_EFFECT }
      if (model.downloadedVersion !== null) {
        return {
          model: { ...model, state: 'downloaded', progress: 100, lastCheckedAt: now() },
          effect: { kind: 'schedule-check', delayMs: policy.pollIntervalMs },
        }
      }
      return {
        model: {
          ...model,
          state: 'error',
          error: event.error,
          errorStage: 'check',
          lastCheckedAt: now(),
        },
        effect: { kind: 'schedule-check', delayMs: policy.pollIntervalMs },
      }
    }
    case 'download-started': {
      if (model.state !== 'available' && model.state !== 'downloading') return { model, effect: NO_EFFECT }
      return { model: { ...model, state: 'downloading', progress: 0 }, effect: NO_EFFECT }
    }
    case 'download-progress': {
      if (model.state !== 'downloading') return { model, effect: NO_EFFECT }
      return { model: { ...model, progress: clampPercent(event.percent) }, effect: NO_EFFECT }
    }
    case 'download-complete': {
      if (model.state !== 'downloading' && model.state !== 'available') return { model, effect: NO_EFFECT }
      return {
        model: {
          ...model,
          state: 'downloaded',
          downloadedVersion: event.version,
          availableVersion: event.version,
          progress: 100,
        },
        effect: { kind: 'schedule-check', delayMs: policy.pollIntervalMs },
      }
    }
    case 'download-failure': {
      if (model.state !== 'downloading' && model.state !== 'available') return { model, effect: NO_EFFECT }
      return {
        model: {
          ...model,
          state: 'error',
          error: event.error,
          errorStage: 'download',
          progress: null,
        },
        effect: { kind: 'schedule-check', delayMs: policy.pollIntervalMs },
      }
    }
    case 'install-request': {
      if (model.state !== 'downloaded') return { model, effect: NO_EFFECT }
      return { model: { ...model, state: 'installing', progress: null }, effect: { kind: 'quit-for-install' } }
    }
    case 'install-failure': {
      if (model.state !== 'installing') return { model, effect: NO_EFFECT }
      return {
        model: {
          ...model,
          state: 'error',
          error: event.error,
          errorStage: 'install',
        },
        effect: NO_EFFECT,
      }
    }
  }
}

function canCheck(state: UpdaterState): boolean {
  return state === 'idle' || state === 'up-to-date' || state === 'error' || state === 'downloaded'
}

function clampPercent(percent: number): number {
  if (!Number.isFinite(percent)) return 0
  return Math.min(100, Math.max(0, percent))
}
