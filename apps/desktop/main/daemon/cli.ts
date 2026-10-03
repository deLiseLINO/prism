import { execFile } from 'node:child_process'

export interface CliResult {
  readonly code: number | null
  readonly stdout: string
  readonly stderr: string
}

export function runCli(binary: string, args: readonly string[], timeoutMs: number): Promise<CliResult> {
  const { promise, resolve } = Promise.withResolvers<CliResult>()
  execFile(binary, [...args], { timeout: timeoutMs, windowsHide: true, encoding: 'utf8' }, (error, stdout, stderr) => {
    if (error === null) {
      resolve({ code: 0, stdout, stderr })
      return
    }
    resolve({ code: typeof error.code === 'number' ? error.code : null, stdout, stderr: stderr.trim() === '' ? error.message : stderr })
  })
  return promise
}
