import type {
  AccountView,
  AccountsView,
  AuthStartView,
  AuthStatusView,
  ComboView,
  CombosView,
  ManagementReply,
  ModelView,
  PoolSettingsView,
  ProviderMutationResponse,
  ProvidersView,
  QuotaResponse,
  RouteWrite,
  RoutesView,
  UsageAccountView,
  UsageView,
} from '@prism/contracts'

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
  const reply = await window.prism.management.call({
    method,
    path,
    ...(body === undefined ? {} : { body }),
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
  deleteProvider(id: string, expectedGeneration: number): Promise<{ generation: number }> {
    return call(
      'DELETE',
      `/api/v1/providers/${encodeURIComponent(id)}?expectedGeneration=${expectedGeneration}`,
    )
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
  combos(): Promise<CombosView> {
    return call('GET', '/api/v1/combos')
  },
  putCombo(id: string, body: ComboWrite): Promise<CombosView> {
    return call('PUT', `/api/v1/combos/${encodeURIComponent(id)}`, body)
  },
  deleteCombo(id: string, expectedGeneration: number): Promise<CombosView> {
    return call(
      'DELETE',
      `/api/v1/combos/${encodeURIComponent(id)}?expectedGeneration=${expectedGeneration}`,
    )
  },
  routes(): Promise<RoutesView> {
    return call('GET', '/api/v1/routes')
  },
  putRoute(key: string, body: RouteWrite): Promise<RoutesView> {
    return call('PUT', `/api/v1/routes/${encodeURIComponent(key)}`, body)
  },
  deleteRoute(key: string, expectedGeneration: number): Promise<RoutesView> {
    return call(
      'DELETE',
      `/api/v1/routes/${encodeURIComponent(key)}?expectedGeneration=${expectedGeneration}`,
    )
  },
  usage(): Promise<UsageView> {
    return call('GET', '/api/v1/usage')
  },
  authStart(provider: string): Promise<AuthStartView> {
    return call('POST', `/api/v1/auth/${encodeURIComponent(provider)}/start`)
  },
  authStatus(provider: string, session: string): Promise<AuthStatusView> {
    const query = session === '' ? '' : `?session=${encodeURIComponent(session)}`
    return call('GET', `/api/v1/auth/${encodeURIComponent(provider)}/status${query}`)
  },
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
  readonly enabled?: boolean | null
  readonly pool?: PoolSettingsView | null
  readonly credential?: string | null
  readonly expectedGeneration: number
}

export interface ComboWrite {
  readonly targets: readonly { readonly provider: string; readonly model: string; readonly weight: number }[]
  readonly strategy: 'failover' | 'round_robin'
  readonly stickyLimit: number
  readonly alias?: string
  readonly nativeAlias?: string
  readonly displayName?: string
  readonly imageInput?: boolean | null
  readonly expectedGeneration: number
}

// Re-export for renderer convenience; the canonical type lives in the contracts package.
export type { RouteWrite }

// Internal narrowings consumed by the views.
export interface UsageAccountOut extends UsageAccountView {}
export interface ComboOut extends ComboView {}