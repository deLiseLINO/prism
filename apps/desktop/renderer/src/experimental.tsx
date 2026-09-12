import { createContext, useContext, useEffect, useState, type ReactNode } from 'react'

const STORAGE_KEY = 'prism-experimental'

export type ExperimentalFlag = 'remoteInstall' | 'agentActions'

const FLAGS: readonly ExperimentalFlag[] = ['remoteInstall', 'agentActions']

export const EXPERIMENTAL_FLAGS: readonly { flag: ExperimentalFlag; label: string; description: string }[] = [
  {
    flag: 'remoteInstall',
    label: 'Remote machines and daemon install',
    description: 'The Machines tab and one-click prismd install on a remote host over key-auth ssh.',
  },
]

// agentActions is described separately: the Experimental screen only shows it
// when the daemon reports agent actions as available (PRISM_AGENT_ACTIONS at
// daemon start). End users never see it.
export const AGENT_ACTIONS_FLAG: { flag: ExperimentalFlag; label: string; description: string } = {
  flag: 'agentActions',
  label: 'Agent install and update',
  description: 'Install, reinstall, and update agent binaries (codex, grok, omp, …) from the Integrations tab. Requires the daemon started with PRISM_AGENT_ACTIONS=1.',
}

type ExperimentalState = Record<ExperimentalFlag, boolean>

const DISABLED: ExperimentalState = { remoteInstall: false, agentActions: false }

function readFlags(): ExperimentalState {
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY)
    if (raw === null) return DISABLED
    const parsed: unknown = JSON.parse(raw)
    if (typeof parsed !== 'object' || parsed === null) return DISABLED
    const source = parsed as Record<string, unknown>
    const state = { ...DISABLED }
    for (const flag of FLAGS) {
      if (source[flag] === true) state[flag] = true
    }
    return state
  } catch {
    return DISABLED
  }
}

export interface ExperimentalFlagsValue {
  readonly flags: ExperimentalState
  readonly setFlag: (flag: ExperimentalFlag, on: boolean) => void
}

const ExperimentalFlagsContext = createContext<ExperimentalFlagsValue>({
  flags: DISABLED,
  setFlag: () => {},
})

export function ExperimentalFlagsProvider({ children }: { readonly children: ReactNode }): JSX.Element {
  const [flags, setFlags] = useState<ExperimentalState>(readFlags)

  useEffect(() => {
    try {
      window.localStorage.setItem(STORAGE_KEY, JSON.stringify(flags))
    } catch {
      return
    }
  }, [flags])

  const value: ExperimentalFlagsValue = {
    flags,
    setFlag: (flag, on) => {
      setFlags((prev) => ({ ...prev, [flag]: on }))
    },
  }
  return <ExperimentalFlagsContext.Provider value={value}>{children}</ExperimentalFlagsContext.Provider>
}

export function useExperimentalFlags(): ExperimentalFlagsValue {
  return useContext(ExperimentalFlagsContext)
}
