import { api } from '@/api/endpoints'
import { ApiError } from '@jailops/shared/api/client'

/** The result of one command in one jail: the output as text (stdout and stderr together, as a console session has them). */
export interface RunResult {
  jail: string
  /** undefined while it runs */
  exit?: number
  /** a jail the command could not be started in: the reason */
  error?: string
  output: string
}

/** The most output of one jail the page keeps; the rest is dropped, as `jailops exec` would scroll it away. */
export const MAX_RUN_OUTPUT = 256 * 1024

/**
 * Run one command line in every named jail through console sessions (extension jail_console), `parallel` at a time,
 * the way `jailops exec -l ... -- sh -c LINE` does. `update` is called whenever a result changes.
 */
export async function runMany(jails: string[], line: string, update: (r: RunResult[]) => void, parallel = 4): Promise<RunResult[]> {
  const results: RunResult[] = jails.map((jail) => ({ jail, output: '' }))
  const publish = () => update(results.map((r) => ({ ...r })))
  publish()
  let next = 0
  const worker = async (): Promise<void> => {
    while (next < results.length) {
      const r = results[next++]
      try {
        await runOne(r, line, publish)
      } catch (e) {
        r.error = e instanceof ApiError ? e.message : String(e)
        publish()
      }
    }
  }
  await Promise.all(Array.from({ length: Math.min(parallel, results.length) }, worker))
  return results
}

async function runOne(r: RunResult, line: string, publish: () => void): Promise<void> {
  const opened = await api.exec.open(r.jail, { argv: ['/bin/sh', '-c', line], tty: false })
  const id = (opened.kind === 'sync' ? (opened.data as { id: string }).id : '') || ''
  if (!id) throw new ApiError({ code: 'internal', message: 'the server opened no session' })
  const dec = new TextDecoder()
  await new Promise<void>((resolve) => {
    let done = false
    const finish = (code: number | undefined) => {
      if (done) return
      done = true
      s.close()
      if (code === undefined) r.error = 'the connection ended before the command did'
      else r.exit = code
      void api.exec.close(r.jail, id).catch(() => undefined)
      publish()
      resolve()
    }
    const s = api.exec.output(r.jail, id, {
      onEvent: (e) => {
        const m = e.metadata as { data?: string; exit?: number }
        if (m?.data && r.output.length < MAX_RUN_OUTPUT) {
          r.output += dec.decode(Uint8Array.from(atob(m.data), (c) => c.charCodeAt(0)), { stream: true })
          publish()
        }
        if (m?.exit !== undefined) finish(m.exit)
      },
      onError: () => finish(undefined),
    })
  })
}

/** The words of the summary line. */
export function tally(r: RunResult[]): { ok: number; failed: number } {
  const ok = r.filter((x) => x.exit === 0).length
  return { ok, failed: r.filter((x) => x.exit !== undefined || x.error).length - ok }
}
