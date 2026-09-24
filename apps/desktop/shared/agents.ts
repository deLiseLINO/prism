import type { AgentsView, AgentId, AgentJob, AgentJobReply, AgentJobRequest, AgentStatus, ManagementReply } from '@prism/contracts'
import type { ManagementProxy } from './management'

const AGENT_IDS: readonly AgentId[] = ['codex', 'grok', 'omp', 'claude', 'pi', 'opencode', 'hermes']

export function parseAgentJobRequest(input: unknown): AgentJobRequest {
  if (typeof input !== 'object' || input === null) {
    throw new Error(`prism: agent request must be an object with id in ${AGENT_IDS.join(', ')}`)
  }
  const record = input as Record<string, unknown>
  const id = record['id']
  if (typeof id !== 'string' || !AGENT_IDS.includes(id as AgentId)) {
    throw new Error(`prism: unknown agent id, expected one of ${AGENT_IDS.join(', ')}`)
  }
  const force = record['force']
  if (force !== undefined && typeof force !== 'boolean') {
    throw new Error('prism: agent force must be a boolean')
  }
  return { id: id as AgentId, ...(force === undefined ? {} : { force }) }
}

export class AgentsApi {
  constructor(private readonly management: ManagementProxy) {}

  async install(request: AgentJobRequest): Promise<AgentJobReply> {
    const query = request.force === true ? '?force=true' : ''
    return this.reply(request.id, await this.management.call({
      method: 'POST',
      path: `/api/v1/agents/${request.id}/install${query}`,
    }), [200, 202])
  }

  async update(request: AgentJobRequest): Promise<AgentJobReply> {
    return this.reply(request.id, await this.management.call({
      method: 'POST',
      path: `/api/v1/agents/${request.id}/update`,
    }), [200, 202])
  }

  async job(request: AgentJobRequest): Promise<AgentJob> {
    const reply = await this.management.call({ method: 'GET', path: `/api/v1/agents/${request.id}/job` })
    if (!reply.ok || reply.status !== 200) {
      throw new Error(errorDetail(reply) ?? `prism: agent job failed with status ${reply.status}`)
    }
    return jobFromBody(reply.body)
  }

  async status(): Promise<AgentsView> {
    const reply = await this.management.call({ method: 'GET', path: '/api/v1/agents' })
    if (!reply.ok || reply.status !== 200) {
      throw new Error(errorDetail(reply) ?? `prism: agents status failed with status ${reply.status}`)
    }
    const body = reply.body as { agents?: unknown; actionsEnabled?: unknown } | undefined
    if (typeof body !== 'object' || body === null || !Array.isArray(body.agents)) {
      throw new Error('prism: agents status response is malformed')
    }
    return {
      agents: body.agents as AgentStatus[],
      actionsEnabled: body.actionsEnabled === true,
    }
  }

  private reply(id: AgentId, raw: ManagementReply, accepted: readonly number[]): AgentJobReply {
    if (raw.ok && accepted.includes(raw.status)) {
      return { ok: true, job: jobFromBody(raw.body) }
    }
    const message = errorDetail(raw)
    return { ok: false, id, reason: message === null ? `prism: ${id} request failed` : message }
  }
}

function jobFromBody(body: unknown): AgentJob {
  if (typeof body !== 'object' || body === null) {
    throw new Error('prism: agent job response is malformed')
  }
  const job = (body as { job?: unknown }).job
  if (typeof job !== 'object' || job === null) {
    throw new Error('prism: agent job response is malformed')
  }
  return job as AgentJob
}

function errorDetail(reply: ManagementReply): string | null {
  if (typeof reply.error === 'string') {
    return reply.error
  }
  const body = reply.body as { error?: { message?: string } } | undefined
  const message = body?.error?.message
  return typeof message === 'string' ? message : null
}
