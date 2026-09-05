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

export function useAsync<T>(load: () => Promise<T>, deps: readonly unknown[]): UseAsyncResult<T> {
  const [state, setState] = useState<AsyncState<T>>({ kind: 'idle' })
  const [tick, setTick] = useState(0)
  const loadRef = useRef(load)
  loadRef.current = load

  useEffect(() => {
    let active = true
    setState({ kind: 'loading' })
    loadRef.current().then(
      (value) => {
        if (!active) return
        setState({ kind: 'ready', value })
      },
      (error: unknown) => {
        if (!active) return
        setState({
          kind: 'error',
          error: error instanceof Error ? error : new Error(String(error)),
        })
      },
    )
    return () => {
      active = false
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
