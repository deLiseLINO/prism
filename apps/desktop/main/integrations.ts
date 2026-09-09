import type {
  HostsView,
  HostView,
  IntegrationApplyResult,
  IntegrationId,
  IntegrationRequest,
  IntegrationStatus,
  ManagementReply,
} from '@prism/contracts'
import type { ManagementProxy } from './management'

const INTEGRATION_IDS: readonly IntegrationId[] = ['codex', 'grok', 'omp', 'claude', 'pi', 'opencode', 'opencode2', 'hermes']

export function parseIntegrationRequest(input: unknown): IntegrationRequest {
  if (typeof input !== 'object' || input === null) {
    throw new Error(`prism: integration request must be an object with id in ${INTEGRATION_IDS.join(', ')}`)
  }
  const record = input as Record<string, unknown>
  const id = record['id']
  if (typeof id !== 'string' || !INTEGRATION_IDS.includes(id as IntegrationId)) {
    throw new Error(`prism: unknown integration id, expected one of ${INTEGRATION_IDS.join(', ')}`)
  }
  const force = record['force']
  if (force !== undefined && typeof force !== 'boolean') {
    throw new Error('prism: integration force must be a boolean')
  }
  const host = record['host']
  if (host !== undefined && (typeof host !== 'string' || host === '')) {
    throw new Error('prism: integration host must be a non-empty string')
  }
  const picked: IntegrationRequest = { id: id as IntegrationId, ...(force === undefined ? {} : { force }), ...(host === undefined ? {} : { host }) }
  return picked
}

export class IntegrationApi {
  constructor(private readonly management: ManagementProxy) {}

  async apply(request: IntegrationRequest): Promise<IntegrationApplyResult> {
    const query = request.force === true ? '?force=true' : ''
    const reply = await this.management.call({
      method: 'POST',
      path: `/api/v1${hostSegment(request.host)}/integrations/${request.id}/apply${query}`,
    })
    return this.result(request.id, reply)
  }

  async rollback(request: IntegrationRequest): Promise<IntegrationApplyResult> {
    const reply = await this.management.call({
      method: 'POST',
      path: `/api/v1${hostSegment(request.host)}/integrations/${request.id}/rollback`,
    })
    return this.result(request.id, reply)
  }

  async status(host?: string): Promise<readonly IntegrationStatus[]> {
    const reply = await this.management.call({ method: 'GET', path: `/api/v1${hostSegment(host)}/integrations` })
    if (!reply.ok || reply.status !== 200) {
      throw new Error(
        errorDetail(reply) ?? `prism: integrations status failed with status ${reply.status}`,
      )
    }
    const body = reply.body as { integrations?: unknown } | undefined
    if (typeof body !== 'object' || body === null || !Array.isArray(body.integrations)) {
      throw new Error('prism: integrations status response is malformed')
    }
    return body.integrations as IntegrationStatus[]
  }

  async hosts(): Promise<HostsView> {
    const reply = await this.management.call({ method: 'GET', path: '/api/v1/hosts' })
    if (!reply.ok || reply.status !== 200) {
      throw new Error(errorDetail(reply) ?? `prism: hosts request failed with status ${reply.status}`)
    }
    const body = reply.body as { hosts?: unknown } | undefined
    if (typeof body !== 'object' || body === null || !Array.isArray(body.hosts)) {
      throw new Error('prism: hosts response is malformed')
    }
    return { hosts: body.hosts as readonly HostView[] }
  }

  private result(id: IntegrationId, reply: ManagementReply): IntegrationApplyResult {
    if (reply.ok && reply.status === 200) {
      return reply.body as IntegrationApplyResult
    }
    const message = errorDetail(reply)
    return { ok: false, id, reason: message === null ? `prism: ${id} request failed` : message }
  }
}

function hostSegment(host: string | undefined): string {
  if (host === undefined || host === '' || host === 'local') return ''
  return `/hosts/${encodeURIComponent(host)}`
}

function errorDetail(reply: ManagementReply): string | null {
  if (typeof reply.error === 'string') {
    return reply.error
  }
  const body = reply.body as { error?: { message?: string } } | undefined
  const message = body?.error?.message
  return typeof message === 'string' ? message : null
}
