import { createContext, useContext, useState, type ReactNode } from 'react'
import type { HostId, HostView } from '@prism/contracts'

export interface ActiveHostValue {
  readonly host: HostId
  readonly setHost: (host: HostId) => void
  // Last hosts list the Machines view loaded from this machine's daemon.
  readonly hosts: readonly HostView[]
  readonly setHosts: (hosts: readonly HostView[]) => void
}

let activeHost: HostId = 'local'

export function currentHost(): HostId {
  return activeHost
}

function setActiveHost(host: HostId): void {
  activeHost = host
}

const ActiveHostContext = createContext<ActiveHostValue>({ host: 'local', setHost: () => {}, hosts: [], setHosts: () => {} })

export function ActiveHostProvider({ children }: { readonly children: ReactNode }): JSX.Element {
  const [host, setHostState] = useState<HostId>('local')
  const [hosts, setHosts] = useState<readonly HostView[]>([])
  const setHost = (next: HostId): void => {
    setActiveHost(next)
    setHostState(next)
  }
  return <ActiveHostContext.Provider value={{ host, setHost, hosts, setHosts }}>{children}</ActiveHostContext.Provider>
}

export function useActiveHost(): ActiveHostValue {
  return useContext(ActiveHostContext)
}

export function RemoteBanner(): JSX.Element {
  const { host, setHost, hosts } = useActiveHost()

  if (host === 'local') return <></>
  const synced = hosts.find((entry) => entry.id === host)?.autoSync === true
  return (
    <div className="banner banner--spaced" role="status">
      <div>
        <p className="banner__title">Managing {host}</p>
        <p className="banner__detail">Views and actions go to that machine's daemon over SSH.</p>
        {synced ? (
          <p className="banner__detail">Settings sync from this machine. Changes made here are replaced on the next sync.</p>
        ) : null}
      </div>
      <div className="banner__action">
        <button type="button" className="btn" onClick={() => setHost('local')}>Back to this machine</button>
      </div>
    </div>
  )
}
