import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const runCliMock = vi.hoisted(() => vi.fn())
const locateMock = vi.hoisted(() => vi.fn())
const stageMock = vi.hoisted(() => vi.fn())
const locateWebuiMock = vi.hoisted(() => vi.fn())
const probeMock = vi.hoisted(() => vi.fn())

vi.mock('../main/daemon/cli', () => ({ runCli: runCliMock }))

vi.mock('../main/daemon/locate', () => ({
  locateDaemon: locateMock,
  stageDaemon: stageMock,
  locateWebui: locateWebuiMock,
}))

vi.mock('../main/daemon/health', () => ({ probeHealth: probeMock }))

interface RecordedStatus {
  readonly state: string
  readonly attempt: number
  readonly lastError: string | null
}

interface CliReply {
  readonly code: number | null
  readonly stdout: string
  readonly stderr: string
}

const URL = 'http://127.0.0.1:4931'
const ok: CliReply = { code: 0, stdout: `${URL}\n`, stderr: '' }
const failure: CliReply = { code: 1, stdout: '', stderr: 'port 4931 is held by another process' }

const options = {
  port: 4931,
  daemonConfigPath: null,
  webuiDir: null,
  logPath: null,
  commandTimeoutMs: 1000,
  healthPollMs: 1000,
  healthProbeTimeoutMs: 50,
  restartBaseMs: 100,
  restartMaxMs: 1000,
  maxRestarts: 3,
  stabilityWindowMs: 60_000,
}

let recorded: RecordedStatus[] = []
let replies: CliReply[] = []

async function makeSupervisor(overrides: Partial<typeof options> = {}): Promise<import('../main/daemon/supervisor').DaemonSupervisor> {
  const { DaemonSupervisor } = await import('../main/daemon/supervisor')
  const supervisor = new DaemonSupervisor(URL, { ...options, ...overrides })
  supervisor.subscribe((status) => {
    recorded.push({ state: status.state, attempt: status.attempt, lastError: status.lastError })
  })
  return supervisor
}

function lastStatus(): RecordedStatus {
  return recorded[recorded.length - 1]
}

function serviceCalls(): string[][] {
  return runCliMock.mock.calls.map((call) => call[1] as string[])
}

describe('DaemonSupervisor service lifecycle', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    recorded = []
    replies = []
    runCliMock.mockReset().mockImplementation(async () => replies.shift() ?? ok)
    locateMock.mockReset().mockReturnValue({ path: '/virtual/bundled', source: 'bundled' })
    stageMock.mockReset().mockResolvedValue('/virtual/staged/prism')
    locateWebuiMock.mockReset().mockReturnValue(null)
    probeMock.mockReset().mockResolvedValue(true)
    vi.resetModules()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('runs the staged binary with service start and the listen address, reading the url from stdout', async () => {
    const supervisor = await makeSupervisor()
    const status = await supervisor.start()
    expect(stageMock).toHaveBeenCalledWith({ path: '/virtual/bundled', source: 'bundled' })
    expect(runCliMock).toHaveBeenCalledWith('/virtual/staged/prism', ['service', 'start', '--listen', '127.0.0.1:4931'], 1000)
    expect(status).toMatchObject({ state: 'ready', attempt: 1, pid: null, endpoint: URL, lastError: null })
    expect(typeof status.startedAt).toBe('string')
  })

  it('forwards the config path and webui directory', async () => {
    locateWebuiMock.mockReturnValue('/virtual/webui')
    const supervisor = await makeSupervisor({ daemonConfigPath: '/virtual/config.yaml' })
    await supervisor.start()
    expect(serviceCalls()[0]).toEqual(['service', 'start', '--listen', '127.0.0.1:4931', '--config', '/virtual/config.yaml', '--webui', '/virtual/webui'])
  })

  it('stages the binary once across repeated launches', async () => {
    const supervisor = await makeSupervisor()
    await supervisor.start()
    await supervisor.restart()
    expect(stageMock).toHaveBeenCalledTimes(1)
    expect(serviceCalls().map((args) => args[1])).toEqual(['start', 'restart'])
  })

  it('leaves the daemon running on quit', async () => {
    const supervisor = await makeSupervisor()
    await supervisor.start()
    runCliMock.mockClear()
    supervisor.release()
    expect(runCliMock).not.toHaveBeenCalled()
    expect(lastStatus()).toMatchObject({ state: 'quitting' })
    await vi.advanceTimersByTimeAsync(60_000)
    expect(runCliMock).not.toHaveBeenCalled()
    await expect(supervisor.start()).rejects.toThrow('quitting')
  })

  it('stop calls service stop and reports stopped', async () => {
    const supervisor = await makeSupervisor()
    await supervisor.start()
    const status = await supervisor.stop()
    expect(serviceCalls()[1]).toEqual(['service', 'stop'])
    expect(status.state).toBe('stopped')
    await vi.advanceTimersByTimeAsync(60_000)
    expect(runCliMock).toHaveBeenCalledTimes(2)
  })

  it('surfaces a failing service stop as failed with stderr', async () => {
    const supervisor = await makeSupervisor()
    await supervisor.start()
    replies = [failure]
    const status = await supervisor.stop()
    expect(status.state).toBe('failed')
    expect(status.lastError).toContain('port 4931 is held by another process')
  })

  it('restarts through service restart with the forwarded flags', async () => {
    const supervisor = await makeSupervisor()
    await supervisor.start()
    const status = await supervisor.restart()
    expect(serviceCalls()[1]).toEqual(['service', 'restart', '--listen', '127.0.0.1:4931'])
    expect(status.state).toBe('ready')
  })

  it('backs off then fails with stderr text when service start keeps failing', async () => {
    replies = [failure, failure, failure]
    const supervisor = await makeSupervisor()
    await supervisor.start()
    expect(lastStatus()).toMatchObject({ state: 'backoff', attempt: 1 })
    await vi.advanceTimersByTimeAsync(199)
    expect(runCliMock).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1)
    expect(runCliMock).toHaveBeenCalledTimes(2)
    expect(lastStatus()).toMatchObject({ state: 'backoff', attempt: 2 })
    await vi.advanceTimersByTimeAsync(400)
    expect(runCliMock).toHaveBeenCalledTimes(3)
    expect(lastStatus()).toMatchObject({ state: 'failed' })
    expect(supervisor.status.lastError).toContain('port 4931 is held by another process')
    expect(supervisor.logTail()).toContain('port 4931 is held by another process')
    await vi.advanceTimersByTimeAsync(10_000)
    expect(runCliMock).toHaveBeenCalledTimes(3)
  })

  it('fails immediately when the binary cannot be staged', async () => {
    stageMock.mockRejectedValue(new Error('prism: cannot stage'))
    const supervisor = await makeSupervisor()
    const status = await supervisor.start()
    expect(status).toMatchObject({ state: 'failed', lastError: 'prism: cannot stage' })
    expect(runCliMock).not.toHaveBeenCalled()
  })

  it('reruns service start with backoff after the daemon stops answering', async () => {
    const supervisor = await makeSupervisor()
    await supervisor.start()
    probeMock.mockResolvedValue(false)
    await vi.advanceTimersByTimeAsync(3000)
    expect(lastStatus()).toMatchObject({ state: 'backoff', attempt: 1 })
    expect(runCliMock).toHaveBeenCalledTimes(1)
    probeMock.mockResolvedValue(true)
    await vi.advanceTimersByTimeAsync(199)
    expect(runCliMock).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1)
    expect(runCliMock).toHaveBeenCalledTimes(2)
    expect(serviceCalls()[1][1]).toBe('start')
    expect(lastStatus()).toMatchObject({ state: 'ready', attempt: 2 })
  })

  it('resets the attempt count when the daemon was stable before disappearing', async () => {
    const supervisor = await makeSupervisor()
    await supervisor.start()
    await vi.advanceTimersByTimeAsync(options.stabilityWindowMs + 1)
    probeMock.mockResolvedValue(false)
    await vi.advanceTimersByTimeAsync(3000)
    expect(lastStatus()).toMatchObject({ state: 'backoff', attempt: 0 })
  })

  it('gives up with failed after repeated disappearances inside the stability window', async () => {
    const supervisor = await makeSupervisor()
    await supervisor.start()
    for (const [attempt, delay] of [[1, 200], [2, 400]]) {
      probeMock.mockResolvedValue(false)
      await vi.advanceTimersByTimeAsync(3000)
      expect(lastStatus()).toMatchObject({ state: 'backoff', attempt })
      probeMock.mockResolvedValue(true)
      await vi.advanceTimersByTimeAsync(delay)
      expect(lastStatus()).toMatchObject({ state: 'ready', attempt: attempt + 1 })
    }
    probeMock.mockResolvedValue(false)
    await vi.advanceTimersByTimeAsync(3000)
    expect(lastStatus()).toMatchObject({ state: 'failed' })
    expect(supervisor.status.lastError).toContain('stopped answering')
  })
})
