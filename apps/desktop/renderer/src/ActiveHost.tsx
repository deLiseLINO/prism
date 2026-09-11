import { createContext, useContext, useState, type ReactNode } from 'react'
import type { HostId } from '@prism/contracts'

export interface ActiveHostValue {
  readonly host: HostId
  readonly setHost: (host: HostId) => void
}

let activeHost: HostId = 'local'

export function currentHost(): HostId {
  return activeHost
}

function setActiveHost(host: HostId): void {
  activeHost = host
}

const ActiveHostContext = createContext<ActiveHostValue>({ host: 'local', setHost: () => {} })

export function ActiveHostProvider({ children }: { readonly children: ReactNode }): JSX.Element {
  const [host, setHostState] = useState<HostId>('local')
  const setHost = (next: HostId): void => {
    setActiveHost(next)
    setHostState(next)
  }
  return <ActiveHostContext.Provider value={{ host, setHost }}>{children}</ActiveHostContext.Provider>
}

export function useActiveHost(): ActiveHostValue {
  return useContext(ActiveHostContext)
}
