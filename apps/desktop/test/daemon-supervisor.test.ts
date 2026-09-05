import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { EventEmitter } from 'node:events'

const spawnMock = vi.hoisted(() => vi.fn())
const locateMock = vi.hoisted(() => vi.fn())
const healthMock = vi.hoisted(() => vi.fn())

vi.mock('node:child_process', () => ({
  spawn: spawnMock,
}))

vi.mock('../main/daemon/locate', () => ({
  locateDaemon: locateMock,
}))

vi.mock('../main/daemon/health', () => ({
  waitForHealth: healthMock,
}))

interface FakeChild extends EventEmitter {
  readonly pid: number
  kill: ReturnType<typeof vi.fn>
}

interface RecordedStatus {
  readonly state: string
  readonly attempt: number
  readonly pid: number | null
  readonly lastError: string | null
}

const options = {
  port: 4931,
  daemonConfigPath: null,
  healthTimeoutMs: 200,
  healthIntervalMs: 20,
  healthProbeTimeoutMs: 50,
  stopGraceMs: 50,
  restartBaseMs: 100,
  restartMaxMs: 1000,
  maxRestarts: 3,
  stabilityWindowMs: 60_000,
}

let children: FakeChild[] = []
let recorded: RecordedStatus[] = []
let healthQueue: Array<'healthy' | 'timeout'> = []

async function makeSupervisor(): Promise<import('../main/daemon/supervisor').DaemonSupervisor> {
  const { DaemonSupervisor } = await import('../main/daemon/supervisor')
  const supervisor = new DaemonSupervisor('http://127.0.0.1:4931', options)
  supervisor.subscribe((status) => {
    recorded.push({ state: status.state, attempt: status.attempt, pid: status.pid, lastError: status.lastError })
  })
  return supervisor
}

function lastStatus(): RecordedStatus {
  return recorded[recorded.length - 1]
}

function setHealth(...outcomes: Array<'healthy' | 'timeout'>): void {
  healthQueue = [...outcomes]
}

function crash(child: FakeChild): void {
  child.emit('exit', 1, null)
}

describe('DaemonSupervisor restart policy', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    children = []
    recorded = []
    healthQueue = []
    spawnMock.mockReset().mockImplementation(() => {
      const child = new EventEmitter() as FakeChild
      Object.defineProperty(child, 'pid', { value: 4242 })
      child.kill = vi.fn(() => true)
      children.push(child)
      return child
    })
    locateMock.mockReset().mockReturnValue({ path: '/virtual/prismd', source: 'bundled' })
    healthMock.mockReset().mockImplementation(() => healthQueue.shift() ?? 'timeout')
    vi.resetModules()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('reaches failed after repeated startup crashes count maxRestarts attempts', async () => {
    const supervisor = await makeSupervisor()
    await supervisor.start()
    expect(spawnMock).toHaveBeenCalledTimes(1)
    expect(children[0].kill).toHaveBeenCalledWith('SIGTERM')
    crash(children[0])
    expect(lastStatus()).toMatchObject({ state: 'backoff', attempt: 1 })
    await vi.advanceTimersByTimeAsync(199)
    expect(spawnMock).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1)
    expect(spawnMock).toHaveBeenCalledTimes(2)
    expect(lastStatus()).toMatchObject({ state: 'starting', attempt: 2 })
    crash(children[1])
    expect(lastStatus()).toMatchObject({ state: 'backoff', attempt: 2 })
    await vi.advanceTimersByTimeAsync(399)
    expect(spawnMock).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(1)
    expect(spawnMock).toHaveBeenCalledTimes(3)
    crash(children[2])
    expect(lastStatus()).toMatchObject({ state: 'failed' })
    const failed = supervisor.status
    expect(failed.state).toBe('failed')
    expect(failed.pid).toBe(null)
    expect(failed.lastExit).toEqual({ code: 1, signal: null })
    expect(failed.lastError).toBe('prism: daemon crashed 3 times without reaching a stable ready state (code=1 signal=none)')
    await vi.advanceTimersByTimeAsync(10_000)
    expect(spawnMock).toHaveBeenCalledTimes(3)
  })

  it('exposes the crashed attempt and its capped backoff delay on every retry event', async () => {
    const { DaemonSupervisor } = await import('../main/daemon/supervisor')
    const cappedOptions = { ...options, maxRestarts: 5 }
    const capped = new DaemonSupervisor('http://127.0.0.1:4931', cappedOptions)
    capped.subscribe((status) => {
      recorded.push({ state: status.state, attempt: status.attempt, pid: status.pid, lastError: status.lastError })
    })
    await capped.start()
    const expectedDelays = [200, 400, 800, 1000]
    for (let crashed = 1; crashed <= 4; crashed++) {
      crash(children[crashed - 1])
      expect(lastStatus()).toMatchObject({ state: 'backoff', attempt: crashed })
      await vi.advanceTimersByTimeAsync(expectedDelays[crashed - 1] - 1)
      expect(spawnMock).toHaveBeenCalledTimes(crashed)
      await vi.advanceTimersByTimeAsync(1)
      expect(spawnMock).toHaveBeenCalledTimes(crashed + 1)
    }
    crash(children[4])
    expect(lastStatus()).toMatchObject({ state: 'failed' })
    expect(capped.status.lastError).toBe('prism: daemon crashed 5 times without reaching a stable ready state (code=1 signal=none)')
  })

  it('reaches ready without scheduling any retry for a healthy process', async () => {
    const supervisor = await makeSupervisor()
    setHealth('healthy')
    const status = await supervisor.start()
    expect(status).toMatchObject({ state: 'ready', attempt: 1, pid: 4242, endpoint: 'http://127.0.0.1:4931' })
    expect(status.lastError).toBe(null)
    expect(status.lastExit).toBe(null)
    expect(typeof status.startedAt).toBe('string')
    expect(children[0].kill).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(10_000)
    expect(spawnMock).toHaveBeenCalledTimes(1)
  })

  it('resets the retry sequence only at the stability window boundary', async () => {
    const supervisor = await makeSupervisor()
    setHealth('healthy', 'timeout', 'timeout')
    await supervisor.start()
    expect(lastStatus()).toMatchObject({ state: 'ready', attempt: 1 })
    await vi.advanceTimersByTimeAsync(options.stabilityWindowMs + 1)
    crash(children[0])
    expect(lastStatus()).toMatchObject({ state: 'backoff', attempt: 0 })
    await vi.advanceTimersByTimeAsync(100)
    expect(spawnMock).toHaveBeenCalledTimes(2)
    expect(lastStatus()).toMatchObject({ state: 'starting', attempt: 1 })
    crash(children[1])
    expect(lastStatus()).toMatchObject({ state: 'backoff', attempt: 1 })
    await vi.advanceTimersByTimeAsync(200)
    expect(spawnMock).toHaveBeenCalledTimes(3)
    expect(lastStatus()).toMatchObject({ state: 'starting', attempt: 2 })
  })

  it('continues the attempt when a ready daemon crashes within the stability window', async () => {
    const supervisor = await makeSupervisor()
    setHealth('healthy')
    await supervisor.start()
    crash(children[0])
    expect(lastStatus()).toMatchObject({ state: 'backoff', attempt: 1 })
    await vi.advanceTimersByTimeAsync(200)
    expect(spawnMock).toHaveBeenCalledTimes(2)
    expect(lastStatus()).toMatchObject({ state: 'starting', attempt: 2 })
  })
})
