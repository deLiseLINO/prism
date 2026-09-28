export type DiagnosticStep =
  | { readonly kind: 'navigate'; readonly view: string; readonly at: number }
  | { readonly kind: 'action'; readonly name: string; readonly at: number }

export interface FailedRequest {
  readonly method: 'GET' | 'POST' | 'PUT' | 'DELETE'
  readonly path: string
  readonly status: number
  readonly code: string
}

export interface ReportContext {
  readonly steps: readonly DiagnosticStep[]
  readonly failed: FailedRequest | null
}

const STEP_LIMIT = 8
const LABEL_LIMIT = 40

const steps: DiagnosticStep[] = []
let failed: FailedRequest | null = null

export function noteStep(step: { readonly kind: 'navigate'; readonly view: string } | { readonly kind: 'action'; readonly name: string }): void {
  const label = step.kind === 'navigate' ? step.view : step.name
  if (!isSafeLabel(label)) return
  steps.push({ ...step, at: Date.now() })
  if (steps.length > STEP_LIMIT) steps.splice(0, steps.length - STEP_LIMIT)
}

export function noteFailedRequest(input: { readonly method: FailedRequest['method']; readonly path: string; readonly status: number; readonly code: string }): void {
  const path = requestPath(input.path)
  if (path === null || !isSafeLabel(input.code) || !Number.isInteger(input.status) || input.status < 0 || input.status > 599) return
  failed = { method: input.method, path, status: input.status, code: input.code }
}

export function reportContext(): ReportContext {
  return { steps: steps.slice(), failed }
}

export function requestPath(raw: string): string | null {
  const cut = raw.split(/[?#]/, 1)[0] ?? ''
  if (cut === '' || cut.includes(':') || cut.includes('\n') || cut.includes('\r')) return null
  return cut
}

function isSafeLabel(value: string): boolean {
  return value !== '' && value.length <= LABEL_LIMIT && !value.includes(':') && !value.includes('\n') && !value.includes('\r')
}
