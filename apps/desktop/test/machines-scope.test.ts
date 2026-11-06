import { describe, expect, it } from 'vitest'
import type { RequestView, RequestsView, StatsOverviewView, StatsResponseView } from '@prism/contracts'
import {
  ALL_MACHINES,
  decodeScope,
  encodeScope,
  fetchMachines,
  initialScope,
  machinesFrom,
  mergeRequests,
  mergeStats,
  scopeCount,
  stackSegments,
  type MachineFetch,
} from '../renderer/src/machines'

function request(seq: number, startedAt: string, model = 'gpt-5.2'): RequestView {
  return {
    seq,
    client: 'codex',
    model,
    startedAt,
    durationMs: 1_000,
    status: 'completed',
    usage: { input: 10, output: 5, cached: 0, reasoning: 0, total: 15 },
    attempts: [],
  }
}

function totals(requests: number, totalTokens: number): StatsOverviewView {
  return {
    requests,
    completed: requests,
    failed: 0,
    input_tokens: totalTokens - 10,
    output_tokens: 10,
    cached_tokens: 0,
    reasoning_tokens: 0,
    total_tokens: totalTokens,
    measured: requests,
  }
}

function ok<T>(machine: string, value: T): MachineFetch<T> {
  return { machine, ok: true, value }
}

const MACHINES = machinesFrom([
  { id: 'local', local: true, status: 'ok' },
  { id: 'edge', local: false, status: 'ok', address: 'user@edge.test' },
  { id: 'gw', local: false, status: 'ok' },
])

describe('machine list', () => {
  it('keeps this machine first plus reachable remotes, labelled for the menu', () => {
    const machines = machinesFrom([
      { id: 'edge', local: false, status: 'ok', address: 'user@edge.test' },
      { id: 'router', local: false, status: 'not_installed', address: 'user@router.test' },
      { id: 'local', local: true, status: 'ok' },
    ])
    expect(machines).toEqual([
      { id: 'local', label: 'This machine', local: true, note: 'this machine' },
      { id: 'edge', label: 'edge', local: false, note: 'user@edge.test' },
    ])
  })
})

describe('merged requests', () => {
  it('interleaves machines newest first and tags every row with its machine', () => {
    const local: RequestsView = {
      requests: [request(2, '2026-11-07T10:00:05Z'), request(1, '2026-11-07T10:00:01Z')],
      dropped: 1,
    }
    const edge: RequestsView = {
      requests: [request(2, '2026-11-07T10:00:09Z'), request(1, '2026-11-07T10:00:03Z')],
      dropped: 2,
    }
    const merged = mergeRequests([ok('local', local), ok('edge', edge)])
    expect(merged.requests.map((row) => `${row.machine}#${row.seq}`)).toEqual(['edge#2', 'local#2', 'edge#1', 'local#1'])
    expect(merged.dropped).toBe(3)
    expect(merged.failures).toEqual([])
    expect(scopeCount(ALL_MACHINES, merged.counts, MACHINES.slice(0, 2))).toBe(4)
    expect(scopeCount({ kind: 'machine', id: 'edge' }, merged.counts, MACHINES)).toBe(2)
  })

  it('keeps the rows of reachable machines when one machine fails', async () => {
    const results = await fetchMachines(['local', 'edge', 'gw'], async (host) => {
      if (host === 'edge') throw new Error('ssh tunnel closed')
      return { requests: [request(1, `2026-11-07T10:00:0${host === 'gw' ? 2 : 1}Z`)], dropped: 0 }
    })
    const merged = mergeRequests(results)
    expect(merged.requests.map((row) => row.machine)).toEqual(['gw', 'local'])
    expect(merged.failures).toEqual([{ machine: 'edge', message: 'ssh tunnel closed' }])
    expect(scopeCount(ALL_MACHINES, merged.counts, MACHINES)).toBeUndefined()
    expect(scopeCount({ kind: 'machine', id: 'gw' }, merged.counts, MACHINES)).toBe(1)
  })
})

describe('merged stats', () => {
  const local: StatsResponseView = {
    range: '24h',
    overview: totals(5, 500),
    providers: [
      { provider: 'codex', ...totals(3, 300) },
      { provider: 'router', ...totals(2, 200) },
    ],
    models: [
      { model: 'gpt-5.2', provider: 'codex', ...totals(3, 300) },
      { model: 'gpt-5.2', provider: 'router', ...totals(2, 200) },
    ],
  }
  const edge: StatsResponseView = {
    range: '24h',
    overview: totals(4, 900),
    providers: [{ provider: 'router', ...totals(4, 900) }],
    models: [{ model: 'gpt-5.2', provider: 'router', ...totals(4, 900) }],
  }

  it('sums overviews and groups providers and models across machines with per-machine tokens', () => {
    const merged = mergeStats([ok('edge', edge), ok('local', local)], ['local', 'edge'])
    expect(merged.overview).toEqual({ ...totals(9, 1_400), input_tokens: 1_380, output_tokens: 20 })
    expect(merged.providers).toEqual([
      { provider: 'router', ...totals(6, 1_100), input_tokens: 1_080, output_tokens: 20, byMachine: { local: 200, edge: 900 } },
      { provider: 'codex', ...totals(3, 300), byMachine: { local: 300 } },
    ])
    expect(merged.models.map((row) => [row.model, row.provider, row.total_tokens, row.byMachine])).toEqual([
      ['gpt-5.2', 'router', 1_100, { local: 200, edge: 900 }],
      ['gpt-5.2', 'codex', 300, { local: 300 }],
    ])
    expect(merged.counts).toEqual(new Map([['local', 5], ['edge', 4]]))
  })

  it('reports a failed machine and still merges the others', () => {
    const merged = mergeStats(
      [ok('local', local), { machine: 'edge', ok: false, message: 'daemon unreachable' }],
      ['local', 'edge'],
    )
    expect(merged.overview).toEqual(local.overview)
    expect(merged.providers.map((row) => row.byMachine)).toEqual([{ local: 300 }, { local: 200 }])
    expect(merged.failures).toEqual([{ machine: 'edge', message: 'daemon unreachable' }])
  })

  it('splits a share bar into machine segments against the largest row', () => {
    const segments = stackSegments({ local: 200, edge: 900 }, MACHINES, 1_100)
    expect(segments.map((segment) => [segment.machine.id, segment.tokens, segment.ratio])).toEqual([
      ['local', 200, 200 / 1_100],
      ['edge', 900, 900 / 1_100],
    ])
    expect(segments[0]!.shade).toBeGreaterThan(segments[1]!.shade)
  })
})

describe('machine scope', () => {
  it('round-trips the persisted scope, including a machine named all', () => {
    expect(decodeScope(encodeScope(ALL_MACHINES))).toEqual(ALL_MACHINES)
    expect(decodeScope(encodeScope({ kind: 'machine', id: 'all' }))).toEqual({ kind: 'machine', id: 'all' })
    expect(decodeScope('edge')).toBeNull()
    expect(decodeScope(null)).toBeNull()
  })

  it('falls back to all machines when the persisted machine is gone', () => {
    expect(initialScope({ activeHost: 'local', stored: { kind: 'machine', id: 'router' }, machines: MACHINES })).toEqual(ALL_MACHINES)
    expect(initialScope({ activeHost: 'local', stored: { kind: 'machine', id: 'gw' }, machines: MACHINES })).toEqual({ kind: 'machine', id: 'gw' })
    expect(initialScope({ activeHost: 'local', stored: null, machines: MACHINES })).toEqual(ALL_MACHINES)
  })

  it('starts on the active remote machine over the persisted choice', () => {
    expect(initialScope({ activeHost: 'edge', stored: { kind: 'machine', id: 'gw' }, machines: MACHINES })).toEqual({ kind: 'machine', id: 'edge' })
  })
})
