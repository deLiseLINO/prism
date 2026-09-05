export interface HealthWaitOptions {
  readonly url: string
  readonly timeoutMs: number
  readonly intervalMs: number
  readonly probeTimeoutMs: number
  readonly isCurrent: () => boolean
}

export type HealthOutcome = 'healthy' | 'timeout'

export async function waitForHealth(options: HealthWaitOptions): Promise<HealthOutcome> {
  const deadline = Date.now() + options.timeoutMs
  while (options.isCurrent()) {
    if (await probeOnce(options.url, options.probeTimeoutMs)) return 'healthy'
    if (Date.now() >= deadline) break
    const remaining = deadline - Date.now()
    const { promise, resolve } = Promise.withResolvers<void>()
    setTimeout(resolve, Math.max(0, Math.min(options.intervalMs, remaining)))
    await promise
  }
  return 'timeout'
}

async function probeOnce(url: string, timeoutMs: number): Promise<boolean> {
  try {
    const response = await fetch(url, { signal: AbortSignal.timeout(timeoutMs) })
    return response.ok
  } catch {
    return false
  }
}
