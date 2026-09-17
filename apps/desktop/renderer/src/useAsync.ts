import { useCallback, useEffect, useRef, useState } from 'react'

export type AsyncState<T> =
  | { readonly kind: 'idle' }
  | { readonly kind: 'loading' }
  | { readonly kind: 'ready'; readonly value: T }
  | { readonly kind: 'error'; readonly error: Error }

export interface UseAsyncResult<T> {
  readonly state: AsyncState<T>
  readonly refresh: () => void
  readonly set: (next: T) => void
}

const RETRY_BASE_MS = 500
const RETRY_MAX_MS = 8_000
const RETRY_MAX_ATTEMPTS = 7

export function useAsync<T>(load: () => Promise<T>, deps: readonly unknown[]): UseAsyncResult<T> {
  const [state, setState] = useState<AsyncState<T>>({ kind: 'idle' })
  const [tick, setTick] = useState(0)
  const hasValueRef = useRef(false)
  const loadRef = useRef(load)
  loadRef.current = load

  useEffect(() => {
    let active = true
    let retryTimer: number | undefined
    const attempt = tick
    setState((prev) => (hasValueRef.current || prev.kind === 'error' ? prev : { kind: 'loading' }))
    loadRef.current().then(
      (value) => {
        if (!active) return
        hasValueRef.current = true
        setState({ kind: 'ready', value })
      },
      (error: unknown) => {
        if (!active) return
        setState({
          kind: 'error',
          error: error instanceof Error ? error : new Error(String(error)),
        })
        if (hasValueRef.current || attempt >= RETRY_MAX_ATTEMPTS) return
        const delay = Math.min(RETRY_BASE_MS * 2 ** attempt, RETRY_MAX_MS)
        retryTimer = window.setTimeout(() => {
          if (active) setTick((n) => n + 1)
        }, delay)
      },
    )
    return () => {
      active = false
      if (retryTimer !== undefined) window.clearTimeout(retryTimer)
    }
  }, [...deps, tick])

  const refresh = useCallback(() => {
    setTick((n) => n + 1)
  }, [])

  const set = useCallback((next: T) => {
    setState({ kind: 'ready', value: next })
  }, [])

  return { state, refresh, set }
}

export interface UseTaskResult {
  readonly running: boolean
  readonly error: Error | null
  readonly run: <T>(task: () => Promise<T>) => Promise<T | undefined>
}

export function useTask(): UseTaskResult {
  const [running, setRunning] = useState(false)
  const [error, setError] = useState<Error | null>(null)
  const runningRef = useRef(false)
  const mountedRef = useRef(true)

  useEffect(() => {
    mountedRef.current = true
    return () => {
      mountedRef.current = false
    }
  }, [])

  const run = useCallback(async <T>(task: () => Promise<T>): Promise<T | undefined> => {
    if (runningRef.current) return undefined
    runningRef.current = true
    setRunning(true)
    setError(null)
    try {
      return await task()
    } catch (err: unknown) {
      const normalized = err instanceof Error ? err : new Error(String(err))
      if (mountedRef.current) setError(normalized)
      return undefined
    } finally {
      runningRef.current = false
      if (mountedRef.current) setRunning(false)
    }
  }, [])

  return { running, error, run }
}

interface CodedError {
  readonly code?: unknown
}

export function describeError(err: Error): string {
  if (typeof err === 'object' && err !== null && 'code' in err) {
    const coded = err as unknown as CodedError
    if (typeof coded.code === 'string') {
      return `${coded.code}: ${err.message}`
    }
  }
  return err.message
}
