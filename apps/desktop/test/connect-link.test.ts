import { describe, expect, it, vi } from 'vitest'

import { PendingConnectLink, findConnectLink } from '../main/connect-link'

const LINK = 'prism://connect?v=1&id=edge&wire=responses&endpoint=http%3A%2F%2F10.0.0.1%3A8080%2Fv1'

describe('findConnectLink', () => {
  it('returns the first connect link in argv', () => {
    const other = 'prism://connect?v=1&id=router&wire=chat&endpoint=https%3A%2F%2Fedge.example%2Fv1'
    expect(findConnectLink(['/opt/prism/prism-desktop', '--no-sandbox', LINK, other])).toBe(LINK)
  })

  it('matches the scheme without regard to case', () => {
    const upper = 'PRISM://connect?v=1&id=edge'
    expect(findConnectLink(['/opt/prism/prism-desktop', upper])).toBe(upper)
  })

  it('returns null when no argument is a connect link', () => {
    expect(findConnectLink([])).toBeNull()
    expect(findConnectLink(['/usr/bin/electron', '.', 'https://edge.example/connect', 'prism://settings', 'xprism://connect'])).toBeNull()
  })
})

describe('PendingConnectLink', () => {
  it('hands a link out once', () => {
    const pending = new PendingConnectLink()
    expect(pending.take()).toBeNull()
    pending.offer(LINK)
    expect(pending.take()).toBe(LINK)
    expect(pending.take()).toBeNull()
  })

  it('keeps only the newest link', () => {
    const pending = new PendingConnectLink()
    pending.offer('prism://connect?v=1&id=old')
    pending.offer(LINK)
    expect(pending.take()).toBe(LINK)
    expect(pending.take()).toBeNull()
  })

  it('notifies subscribers and stops after unsubscribe', () => {
    const pending = new PendingConnectLink()
    const listener = vi.fn()
    const unsubscribe = pending.subscribe(listener)
    pending.offer(LINK)
    expect(listener).toHaveBeenCalledWith(LINK)
    unsubscribe()
    pending.offer('prism://connect?v=1&id=gw')
    expect(listener).toHaveBeenCalledTimes(1)
  })

  it('keeps the link pending after notifying subscribers', () => {
    const pending = new PendingConnectLink()
    pending.subscribe(() => undefined)
    pending.offer(LINK)
    expect(pending.take()).toBe(LINK)
  })
})
