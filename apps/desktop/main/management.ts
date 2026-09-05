import type { ManagementCall, ManagementMethod, ManagementReply } from '@prism/contracts'

const MANAGEMENT_METHODS: readonly ManagementMethod[] = ['GET', 'POST', 'PUT', 'DELETE']
const MANAGEMENT_PATH_PREFIX = '/api/v1/'
const MANAGEMENT_PATH_SEGMENT = /^[A-Za-z0-9_{}.%+-]+$/
const MANAGEMENT_QUERY_KEYS = new Set(['expectedGeneration', 'session'])
const MANAGEMENT_TIMEOUT_MS = 10_000

export class ManagementProxy {
  constructor(private readonly baseUrl: string) {}

  async call(request: ManagementCall): Promise<ManagementReply> {
    try {
      const response = await fetch(this.baseUrl + request.path, {
        method: request.method,
        headers: request.body === undefined ? undefined : { 'content-type': 'application/json' },
        body: request.body === undefined ? undefined : JSON.stringify(request.body),
        signal: AbortSignal.timeout(MANAGEMENT_TIMEOUT_MS),
      })
      const text = await response.text()
      let body: unknown
      try {
        body = JSON.parse(text)
      } catch {
        body = text
      }
      return { ok: response.ok, status: response.status, body }
    } catch (error) {
      return { ok: false, status: 0, error: error instanceof Error ? error.message : String(error) }
    }
  }
}

export function validateManagementCall(input: unknown): ManagementCall {
  if (typeof input !== 'object' || input === null) {
    throw new Error('prism: management request must be an object with method and path')
  }
  const record = input as Record<string, unknown>
  const method = record['method']
  const path = record['path']
  const body = record['body']
  if (typeof method !== 'string' || !MANAGEMENT_METHODS.includes(method as ManagementMethod)) {
    throw new Error(`prism: management method must be one of ${MANAGEMENT_METHODS.join(', ')}`)
  }
  if (typeof path !== 'string' || !isManagementPath(path)) {
    throw new Error('prism: management path must be a safe /api/v1/ route')
  }
  if (body !== undefined && (method === 'GET' || method === 'DELETE')) {
    throw new Error('prism: management body requires POST or PUT')
  }
  if (body !== undefined && !isJsonSerializable(body)) {
    throw new Error('prism: management body must be JSON-serializable')
  }
  return {
    method: method as ManagementMethod,
    path,
    ...(body === undefined ? {} : { body }),
  }
}

function isManagementPath(path: string): boolean {
  const parts = path.split('?')
  if (parts.length > 2) return false
  const pathname = parts[0]
  if (pathname === undefined || !pathname.startsWith(MANAGEMENT_PATH_PREFIX)) return false
  const segments = pathname.slice(MANAGEMENT_PATH_PREFIX.length).split('/')
  if (segments.length === 0 || segments.some((segment) => segment === '')) return false
  for (const segment of segments) {
    if (!MANAGEMENT_PATH_SEGMENT.test(segment)) return false
    try {
      const decoded = decodeURIComponent(segment)
      if (decoded === '.' || decoded === '..') return false
    } catch {
      return false
    }
  }
  const query = parts[1]
  if (query === undefined) return true
  if (query === '') return false
  const params = new URLSearchParams(query)
  let count = 0
  for (const [key, value] of params) {
    if (!MANAGEMENT_QUERY_KEYS.has(key) || value === '') return false
    count += 1
  }
  return count > 0
}

function isJsonSerializable(value: unknown): boolean {
  try {
    return JSON.stringify(value) !== undefined
  } catch {
    return false
  }
}
