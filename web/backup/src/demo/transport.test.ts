import { describe, expect, it } from 'vitest'
import { Client, ApiError } from '@jailops/shared/api/client'
import { demoTransport } from './transport'

const client = (role = 'admin') => new Client(demoTransport({ speed: 1000, role }))
const code = (p: Promise<unknown>) => p.then(() => '', (e: ApiError) => e.code)

describe('demo backup node (real route shapes)', () => {
  it('reports the backup role and the node certificate', async () => {
    const c = client()
    expect((await c.get<{ role: string }>('')).role).toBe('backup')
    expect((await c.get<{ fingerprint: string }>('/certificate')).fingerprint).toMatch(/^SHA256:/)
  })
  it('enforces roles like the node', async () => {
    expect(await code(client('readonly').mutate('POST', '/jobs/gc', {}))).toBe('forbidden')
    expect(await code(client('backup-admin').get('/audit'))).toBe('forbidden')
    expect(await code(client('backup-admin').mutate('POST', '/hosts', { name: 'x', fingerprint: 'y' }))).toBe('forbidden')
    expect(await code(client('admin').get('/audit'))).toBe('')
  })
  it('refuses to execute a deletion before its delay and to request one for a protected snapshot', async () => {
    const c = client()
    expect(await code(c.mutate('POST', '/groups/bsd01.web/snapshots/20261019021500-c4410aa2/deletion/execute'))).toBe('busy')
    expect(await code(c.mutate('POST', '/groups/bsd01.web/snapshots/20261020021500-d8e2c7b9/deletion', {}))).toBe('busy')
  })
  it('runs a retention dry run as a job and deletes nothing', async () => {
    const c = client()
    const before = (await c.list('/groups/bsd01.web/snapshots')).length
    const r = await c.mutate<{ id: number }>('POST', '/jobs/retention', { dry_run: true })
    expect(r.kind === 'sync' && r.data.id).toBeGreaterThan(0)
    await new Promise((res) => setTimeout(res, 20))
    const j = await c.get<{ state: string; result: { dry_run: boolean; groups: unknown[] } }>(`/jobs/${(r as { data: { id: number } }).data.id}`)
    expect(j.state).toBe('succeeded')
    expect(j.result.dry_run).toBe(true)
    expect((await c.list('/groups/bsd01.web/snapshots')).length).toBe(before)
  })
  it('allows one repository-wide job at a time', async () => {
    const c = new Client(demoTransport({ speed: 0.001 }))
    await c.mutate('POST', '/jobs/gc', { dry_run: true })
    expect(await code(c.mutate('POST', '/jobs/verify', { mode: 'new' }))).toBe('busy')
  })
})
