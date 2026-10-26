import { describe, expect, it } from 'vitest'
import { attention } from './attention'
import { allows, who } from './roles'
import { jobToOp } from './jobs'
import type { Repository, Job } from '@/types'

const repo = (o: Partial<Repository> = {}): Repository => ({
  id: 'r', mode: 'online', backend: 'directory', snapshots: 1, chunks: 1, bytes: 1, quarantined: 0, open_sessions: 0, groups: 1, failed_snapshots: 0, protected_snapshots: 0, pending_deletions: 0,
  last_verify: new Date().toISOString(), status: {} as never, next_runs: {}, ...o,
})

describe('attention', () => {
  it('is empty for a healthy node', () => expect(attention({ repo: repo() })).toEqual([]))
  it('lists danger before warnings', () => {
    const a = attention({ repo: repo({ quarantined: 2, mode: 'read-only', failed_snapshots: 1, pending_deletions: 1 }) })
    expect(a.map((x) => x.key)).toEqual(['quar', 'failed', 'mode', 'pend'])
  })
  it('flags never-verified, old verification and stale hosts, and ignores revoked hosts', () => {
    const now = Date.parse('2026-10-21T00:00:00Z')
    expect(attention({ repo: repo({ last_verify: '0001-01-01T00:00:00Z' }) }, now).map((x) => x.key)).toContain('verify-never')
    expect(attention({ repo: repo({ last_verify: '2026-09-01T00:00:00Z' }) }, now).map((x) => x.key)).toContain('verify')
    const host = (name: string, revoked?: string) => ({ id: name, name, fingerprint: 'x', scopes: [], source: 'api', created_at: '', last_seen_at: '2026-10-10T00:00:00Z', revoked_at: revoked })
    const keys = attention({ repo: repo({ last_verify: '2026-10-20T00:00:00Z' }), hosts: [host('a'), host('b', '2026-10-01T00:00:00Z')] }, now).map((x) => x.key)
    expect(keys).toEqual(['stale:a'])
  })
})

describe('roles', () => {
  it('orders readonly < backup-admin < admin', () => {
    expect(allows('readonly', 'backup-admin')).toBe(false)
    expect(allows('backup-admin', 'backup-admin')).toBe(true)
    expect(allows('backup-admin', 'admin')).toBe(false)
    expect(allows('admin', 'backup-admin')).toBe(true)
  })
  it('lets an unknown role try, the server decides', () => expect(allows('', 'admin')).toBe(true))
  it('names who may', () => {
    expect(who('admin')).toBe('admin')
    expect(who('backup-admin')).toBe('backup-admin or admin')
  })
})

describe('jobs', () => {
  const job = (state: Job['state'], extra: Partial<Job> = {}): Job => ({ id: 7, kind: 'gc', state, created_at: '2026-01-01T00:00:00Z', ...extra })
  it('maps job states to operation states', () => {
    expect(jobToOp(job('queued')).status).toBe('pending')
    expect(jobToOp(job('running')).status).toBe('running')
    expect(jobToOp(job('succeeded', { result: { removed: 1 } })).metadata).toEqual({ removed: 1 })
    expect(jobToOp(job('failed', { error: 'boom' })).error?.message).toBe('boom')
    expect(jobToOp(job('running')).may_cancel).toBe(true)
    expect(jobToOp(job('succeeded')).may_cancel).toBe(false)
  })
})
