import { createContext, useContext, useEffect, useState, type ReactNode } from 'react'
import { api } from './api'

// Before the daemon stored the flags, each window kept them under this key.
// The first load moves them to the daemon and drops the key.
const LEGACY_STORAGE_KEY = 'prism-experimental'
const RETRY_MS = 2000

export type ExperimentalFlag = 'remoteInstall' | 'agentActions' | 'visionSidecar' | 'otherAgents' | 'rcChannel'

const FLAGS: readonly ExperimentalFlag[] = ['remoteInstall', 'agentActions', 'visionSidecar', 'otherAgents', 'rcChannel']

export const EXPERIMENTAL_FLAGS: readonly { flag: ExperimentalFlag; label: string }[] = [
  {
    flag: 'agentActions',
    label: 'Agent install and update',
  },
  {
    flag: 'otherAgents',
    label: 'Other agents',
  },
  {
    flag: 'visionSidecar',
    label: 'Vision sidecar',
  },
  {
    flag: 'remoteInstall',
    label: 'Remote machines and daemon install',
  },
  {
    flag: 'rcChannel',
    label: 'RC update channel',
  },
]

type ExperimentalState = Record<ExperimentalFlag, boolean>

const DISABLED: ExperimentalState = { remoteInstall: false, agentActions: false, visionSidecar: false, otherAgents: false, rcChannel: false }

function fromStored(stored: Readonly<Record<string, unknown>>): ExperimentalState {
  const state = { ...DISABLED }
  for (const flag of FLAGS) {
    if (stored[flag] === true) state[flag] = true
  }
  return state
}

function legacyFlags(): ExperimentalFlag[] {
  try {
    const raw = window.localStorage.getItem(LEGACY_STORAGE_KEY)
    if (raw === null) return []
    const parsed: unknown = JSON.parse(raw)
    if (typeof parsed !== 'object' || parsed === null) return []
    const state = fromStored(parsed as Record<string, unknown>)
    return FLAGS.filter((flag) => state[flag])
  } catch {
    return []
  }
}

function dropLegacyFlags(): void {
  try {
    window.localStorage.removeItem(LEGACY_STORAGE_KEY)
  } catch {
    return
  }
}

async function loadFlags(): Promise<ExperimentalState> {
  let view = await api.experimental()
  for (const flag of legacyFlags()) {
    if (view.flags[flag] !== true) view = await api.setExperimentalFlag(flag, true)
  }
  dropLegacyFlags()
  return fromStored(view.flags)
}

export interface ExperimentalFlagsValue {
  readonly flags: ExperimentalState
  readonly setFlag: (flag: ExperimentalFlag, on: boolean) => void
}

const ExperimentalFlagsContext = createContext<ExperimentalFlagsValue>({
  flags: DISABLED,
  setFlag: () => {},
})

// The local daemon stores the flags, so the desktop app and the web UI of one
// machine show the same set. Until the daemon answers, every flag reads off.
export function ExperimentalFlagsProvider({ children }: { readonly children: ReactNode }): JSX.Element {
  const [flags, setFlags] = useState<ExperimentalState>(DISABLED)

  useEffect(() => {
    let active = true
    let timer: number | undefined
    const load = (): void => {
      loadFlags().then(
        (loaded) => {
          if (active) setFlags(loaded)
        },
        () => {
          if (active) timer = window.setTimeout(load, RETRY_MS)
        },
      )
    }
    load()
    return () => {
      active = false
      window.clearTimeout(timer)
    }
  }, [])

  const value: ExperimentalFlagsValue = {
    flags,
    setFlag: (flag, on) => {
      setFlags((prev) => ({ ...prev, [flag]: on }))
      api.setExperimentalFlag(flag, on).then(
        (view) => setFlags(fromStored(view.flags)),
        () => setFlags((prev) => ({ ...prev, [flag]: !on })),
      )
    },
  }
  return <ExperimentalFlagsContext.Provider value={value}>{children}</ExperimentalFlagsContext.Provider>
}

export function useExperimentalFlags(): ExperimentalFlagsValue {
  return useContext(ExperimentalFlagsContext)
}
