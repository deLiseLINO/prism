import type { Unsubscribe } from '@prism/contracts'

export const CONNECT_LINK_SCHEME = 'prism'

const CONNECT_LINK_PREFIX = `${CONNECT_LINK_SCHEME}://connect`

export function isConnectLink(value: string): boolean {
  return value.slice(0, CONNECT_LINK_PREFIX.length).toLowerCase() === CONNECT_LINK_PREFIX
}

export function findConnectLink(argv: readonly string[]): string | null {
  return argv.find(isConnectLink) ?? null
}

// Holds the newest link until the renderer takes it, so a link that arrives
// before the window subscribes is still delivered, and only once.
export class PendingConnectLink {
  private link: string | null = null
  private readonly listeners = new Set<(link: string) => void>()

  offer(link: string): void {
    this.link = link
    for (const listener of [...this.listeners]) listener(link)
  }

  take(): string | null {
    const link = this.link
    this.link = null
    return link
  }

  subscribe(listener: (link: string) => void): Unsubscribe {
    this.listeners.add(listener)
    return () => {
      this.listeners.delete(listener)
    }
  }
}
