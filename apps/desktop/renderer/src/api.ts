import { currentHost } from './ActiveHost'
import type {
  AccountView,
  AccountsView,
  AuthStartView,
  AuthStatusView,
  HostMutationResponse,
  HostsView,
  HostWrite,
  IntegrationApplyResult,
  IntegrationsView,
  IntegrationToggleResponse,
  IntegrationToggleWrite,
  ManagementReply,
  ModelView,
  PoolSettingsView,
  ProviderModelMode,
  ProviderMutationResponse,
  ProvidersView,

  QuotaResponse,
  StatsRange,
  StatsResponseView,
  RequestsView,
  UsageAccountView,
  UsageView,
  VisionSidecarWrite,
  ContextWindowWrite,
} from '@prism/contracts'
import { bridge } from './bridge'

export class ApiError extends Error {
  constructor(
    public readonly status: number,
    public readonly code: string,
    message: string,
  ) {
    super(message)
    this.name = 'ApiError'
  }
}

function asError(reply: ManagementReply): ApiError {
  if (typeof reply.body === 'object' && reply.body !== null && 'error' in reply.body) {
    const body = reply.body as { error?: { code?: string; message?: string } }
    const code = body.error?.code ?? 'unknown'
    const message = body.error?.message ?? reply.error ?? `prism: management call failed with status ${reply.status}`
    return new ApiError(reply.status, code, message)
  }
  const message = reply.error ?? `prism: management call failed with status ${reply.status}`
  return new ApiError(reply.status, 'unknown', message)
}

function unwrap<T>(reply: ManagementReply, expected = 200): T {
  if (reply.ok && reply.status === expected) {
    return reply.body as T
  }
  throw asError(reply)
}

async function call<T>(
  method: 'GET' | 'POST' | 'PUT' | 'DELETE',
  path: string,
  body?: unknown,
  expectedStatus = 200,
): Promise<T> {
  const host = currentHost()
  const reply = await bridge.management.call({
    method,
    path,
    ...(body === undefined ? {} : { body }),
    ...(host !== 'local' ? { host } : {}),
  })
  return unwrap<T>(reply, expectedStatus)
}


interface AccountMutationWrite {
  readonly version: number
}

interface PriorityWrite {
  readonly version: number
  readonly priority: number
}

export const api = {
  health(): Promise<{ status: string }> {
    return call('GET', '/api/v1/health')
  },
  models(): Promise<{ models: readonly ModelView[] }> {
    return call('GET', '/api/v1/models')
  },
  providers(): Promise<ProvidersView> {
    return call('GET', '/api/v1/providers')
  },
  createProvider(body: ProviderWrite): Promise<ProviderMutationResponse> {
    return call('POST', '/api/v1/providers', body)
  },
  replaceProvider(id: string, body: ProviderWrite): Promise<ProviderMutationResponse> {
    return call('PUT', `/api/v1/providers/${encodeURIComponent(id)}`, body)
  },
  setProviderModelMode(
    id: string,
    mode: ProviderModelMode,
    expectedGeneration: number,
  ): Promise<ProviderMutationResponse> {
    return call(
      'PUT',
      `/api/v1/providers/${encodeURIComponent(id)}/model-mode?expectedGeneration=${expectedGeneration}`,
      { mode, expectedGeneration },
    )
  },
  syncProviderModels(id: string, expectedGeneration: number): Promise<ProviderMutationResponse> {
    return call(
      'POST',
      `/api/v1/providers/${encodeURIComponent(id)}/sync-models?expectedGeneration=${expectedGeneration}`,
    )
  },
  deleteProvider(id: string, expectedGeneration: number): Promise<{ generation: number }> {
    return call(
      'DELETE',
      `/api/v1/providers/${encodeURIComponent(id)}?expectedGeneration=${expectedGeneration}`,
    )
  },
  visionSidecar(body: VisionSidecarWrite): Promise<VisionSidecarWrite> {
    return call('PUT', '/api/v1/vision-sidecar', body)
  },
  contextWindow(body: ContextWindowWrite): Promise<ContextWindowWrite> {
    return call('PUT', '/api/v1/context-window', body)
  },
  accounts(): Promise<AccountsView> {
    return call('GET', '/api/v1/accounts')
  },
  pauseAccount(id: string, version: number): Promise<AccountView> {
    const body: AccountMutationWrite = { version }
    return call('POST', `/api/v1/accounts/${encodeURIComponent(id)}/pause`, body)
  },
  resumeAccount(id: string, version: number): Promise<AccountView> {
    const body: AccountMutationWrite = { version }
    return call('POST', `/api/v1/accounts/${encodeURIComponent(id)}/resume`, body)
  },
  setPriority(id: string, version: number, priority: number): Promise<AccountView> {
    const body: PriorityWrite = { version, priority }
    return call('POST', `/api/v1/accounts/${encodeURIComponent(id)}/priority`, body)
  },
  deleteAccount(id: string): Promise<void> {
    return call('DELETE', `/api/v1/accounts/${encodeURIComponent(id)}`, undefined, 204)
  },
  quota(id: string): Promise<QuotaResponse> {
    return call('GET', `/api/v1/accounts/${encodeURIComponent(id)}/quota`)
  },
  refreshQuota(id: string): Promise<QuotaResponse> {
    return call('POST', `/api/v1/accounts/${encodeURIComponent(id)}/quota/refresh`)
  },
  usage(): Promise<UsageView> {
    return call('GET', '/api/v1/usage')
  },
  hosts(): Promise<HostsView> {
    return call('GET', '/api/v1/hosts')
  },
  createHost(body: HostWrite): Promise<HostMutationResponse> {
    return call('POST', '/api/v1/hosts', body)
  },
  deleteHost(id: string, expectedGeneration: number): Promise<{ generation: number }> {
    return call('DELETE', `/api/v1/hosts/${encodeURIComponent(id)}?expectedGeneration=${expectedGeneration}`)
  },
  stats(range: StatsRange): Promise<StatsResponseView> {
    return call('GET', `/api/v1/stats?range=${encodeURIComponent(range)}`)
  },
  requests(limit?: number): Promise<RequestsView> {
    const query = limit === undefined ? '' : `?limit=${limit}`
    return call('GET', `/api/v1/requests${query}`)
  },

  authStart(provider: string): Promise<AuthStartView> {
    return call('POST', `/api/v1/auth/${encodeURIComponent(provider)}/start`)
  },
  authStatus(provider: string, session: string): Promise<AuthStatusView> {
    const query = session === '' ? '' : `?session=${encodeURIComponent(session)}`
    return call('GET', `/api/v1/auth/${encodeURIComponent(provider)}/status${query}`)
  },

  integrationsStatus(): Promise<IntegrationsView> {
    return call('GET', '/api/v1/integrations')
  },

  integrationToggle(id: string, body: IntegrationToggleWrite): Promise<IntegrationToggleResponse> {
    return call('PUT', `/api/v1/integrations/${encodeURIComponent(id)}/enabled`, body)
  },

  integrationApply(id: string, force: boolean): Promise<IntegrationApplyResult> {
    return call('POST', `/api/v1/integrations/${encodeURIComponent(id)}/apply${force ? '?force=true' : ''}`)
  },
  integrationRollback(id: string): Promise<IntegrationApplyResult> {
    return call('POST', `/api/v1/integrations/${encodeURIComponent(id)}/rollback`)
  },
  localHosts(): Promise<HostsView> {
    return localCall('GET', '/api/v1/hosts')
  },

  localProviders(): Promise<ProvidersView> {
    return localCall('GET', '/api/v1/providers')
  },

  localCreateHost(body: HostWrite): Promise<HostMutationResponse> {
    return localCall('POST', '/api/v1/hosts', body)
  },

  localDeleteHost(id: string, expectedGeneration: number): Promise<{ generation: number }> {
    return localCall('DELETE', `/api/v1/hosts/${encodeURIComponent(id)}?expectedGeneration=${expectedGeneration}`)
  },
}


async function localCall<T>(method: 'GET' | 'POST' | 'PUT' | 'DELETE', path: string, body?: unknown, expectedStatus = 200): Promise<T> {
  const reply = await window.prism.management.call({ method, path, ...(body === undefined ? {} : { body }) })
  return unwrap<T>(reply, expectedStatus)
}




// DTOs the renderer ships to the daemon. Mirrors internal/management/schema.go shapes.
// Optional `| null` variants are deliberate: the daemon treats a JSON null exactly like an
// absent key for these pointer fields (non-destructive merge), and callers that round-trip a
// fetched provider keep their values byte-identical. Views omit keys the user did not touch.
export interface ProviderWrite {
  readonly id: string
  readonly wire: string
  readonly baseURL?: string | null
  readonly apiKeyRef?: string | null
  readonly defaultModel?: string | null
  readonly models: readonly string[]
  readonly disabledModels: readonly string[]
  readonly syncedModels?: readonly string[] | null
  readonly modelSettings?: Readonly<Record<string, ModelSettingsView>> | null
  readonly enabled?: boolean | null
  readonly pool?: PoolSettingsView | null
  readonly credential?: string | null
  readonly expectedGeneration: number
}

export interface ModelSettingsView {
  readonly contextWindow?: number
  readonly imageInput?: boolean
  readonly reasoningEfforts?: readonly string[]
}

// Internal narrowings consumed by the views.
export interface UsageAccountOut extends UsageAccountView {}
