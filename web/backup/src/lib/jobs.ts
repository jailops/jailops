import { api } from '@/api/endpoints'
import { runOp } from '@jailops/shared/composables/ops'
import type { MutationResult, Operation } from '@jailops/shared/api/types'
import type { Job } from '@/types'

/** Jobs are not api.Operations: the node answers 202 with the job and the UI polls GET /jobs/{id}. Map it
 *  so the shared toast flow (progress, cancel, error panel) works unchanged. */
export function jobToOp(j: Job): Operation {
  const status = j.state === 'succeeded' ? 'success' : j.state === 'failed' ? 'failure' : j.state === 'cancelled' ? 'cancelled' : j.state === 'queued' ? 'pending' : 'running'
  return {
    id: String(j.id),
    description: `${j.kind} #${j.id}`,
    status,
    metadata: j.result as never,
    error: j.error ? { code: 'internal', message: j.error } : undefined,
    may_cancel: j.state === 'queued' || j.state === 'running',
    created_at: j.created_at,
    updated_at: j.finished_at ?? j.started_at ?? j.created_at,
  }
}

/** Start a job and follow it to the end. The promise resolves with the finished job's result. */
export async function runJob<R = unknown>(title: string, start: () => Promise<MutationResult<Job>>, opts: { silent?: boolean } = {}): Promise<{ job: Job | null; result: R | undefined }> {
  let last: Job | null = null
  const r = await runOp(
    title,
    async () => {
      const m = await start()
      if (m.kind !== 'sync') return m
      last = m.data
      return { kind: 'async' as const, op: jobToOp(m.data) }
    },
    {
      silent: opts.silent,
      poll: async (id) => {
        last = await api.jobs.get(Number(id))
        return jobToOp(last)
      },
      cancel: async (id) => {
        await api.jobs.cancel(Number(id))
      },
    },
  )
  const job = last as Job | null
  return { job, result: (r.op?.metadata ?? job?.result) as R | undefined }
}
