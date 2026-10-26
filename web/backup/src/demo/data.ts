// Canned data for `?demo` of the backup-node console, in the shapes of internal/backupd/api.go.
import type { ServerInfo } from '@jailops/shared/api/types'
import type { Repository, Host, Group, Snapshot, BackupSession, Job, QuarantinedChunk, RetentionPolicyRow, NodeAuditEntry, NodeUser, NodeToken } from '@/types'

const now = Date.now()
const ago = (sec: number) => new Date(now - sec * 1000).toISOString()
const ahead = (sec: number) => new Date(now + sec * 1000).toISOString()
const GiB = 1024 ** 3
const MiB = 1024 ** 2

export function server(): ServerInfo {
  return {
    api_version: '1.0', api_extensions: [], version: '0.1.0-demo', host_uuid: '9c1d7e10-aaaa-4b0b-8a5c-0123456789cd',
    hostname: 'vault.example.net', role: 'backup', auth: 'trusted',
    environment: { os: 'FreeBSD', kernel_version: '15.1-RELEASE', arch: 'amd64', zfs_version: '2.3.0' },
  }
}

export const NODE_FINGERPRINT = 'SHA256:9c:1d:7e:10:aa:aa:4b:0b:8a:5c:01:23:45:67:89:cd:ef:01:23:45:67:89:ab:cd:ef:01:23:45:67:89:ab:cd'

export function repository(snaps: Snapshot[], quarantined: number, mode: string, sessions: number): Repository {
  const failed = snaps.filter((x) => x.verify_state === 'failed').length
  const st = { id: 'repo-5b2e1f09', mode, backend: 'directory', snapshots: snaps.length, chunks: 482113, bytes: 96 * GiB, last_gc: ago(86400 * 2), last_verify: ago(86400 * 6), quarantined, open_sessions: sessions }
  return {
    ...st, groups: 4, failed_snapshots: failed, protected_snapshots: snaps.filter((x) => x.protected).length, pending_deletions: snaps.filter((x) => x.pending_deletion_after).length,
    status: st,
    next_runs: { retention: ahead(3600 * 3), verify: ahead(86400 * 1), gc: ahead(86400 * 5), reconcile: ahead(1200) },
  }
}

export function hosts(): Host[] {
  const h = (id: string, name: string, fp: string, seen: number, extra: Partial<Host> = {}): Host => ({ id, name, fingerprint: fp, scopes: ['backup-push', 'backup-pull'], source: 'api', created_at: ago(86400 * 40), last_seen_at: ago(seen), ...extra })
  return [
    h('h1', 'bsd01', 'SHA256:3b:9f:aa:01:c2:77:de:90:11:5e:42:ab:cd:ef:01:77', 1800, { note: 'production host' }),
    h('h2', 'bsd02', 'SHA256:aa:bb:cc:dd:ee:ff:00:11:22:33:44:55:66:77:88:99', 3600 * 5),
    h('h3', 'bsd03', 'SHA256:cc:dd:ee:ff:00:11:22:33:44:55:66:77:88:99:aa:bb', 86400 * 4, { scopes: ['backup-push'] }),
    h('h4', 'old-box', 'SHA256:00:11:22:33:44:55:66:77:88:99:aa:bb:cc:dd:ee:ff', 86400 * 60, { revoked_at: ago(86400 * 30) }),
  ]
}

export function groups(snaps: Snapshot[]): Group[] {
  const g = (id: string, host: string, created: number): Group => {
    const mine = snaps.filter((x) => x.group_id === id)
    return {
      group_id: id, host_id: 'h-' + host, host_name: host, created_at: ago(created), last_activity: mine[0]?.committed_at,
      snapshots: mine.length, failed: mine.filter((x) => x.verify_state === 'failed').length, protected: mine.filter((x) => x.protected).length,
      pending_deletion: mine.filter((x) => x.pending_deletion_after).length, latest: mine.map((x) => x.committed_at).sort().at(-1),
    }
  }
  return [g('bsd01.web', 'bsd01', 86400 * 40), g('bsd01.db', 'bsd01', 86400 * 40), g('bsd02.mail', 'bsd02', 86400 * 30), g('bsd02.build', 'bsd02', 86400 * 25)]
}

function snap(group: string, id: string, age: number, chunks: number, st: Partial<Snapshot> = {}): Snapshot {
  return { group_id: group, snapshot_id: id, committed_at: ago(age), chunk_count: chunks, index_bytes: chunks * 48, source: 'host-push', protected: false, verify_state: 'ok', verified_at: ago(Math.max(60, age - 900)), ...st }
}
export function snapshots(): Snapshot[] {
  return [
    snap('bsd01.web', '20261020021500-d8e2c7b9', 86400, 2410, { protected: true }),
    snap('bsd01.web', '20261019021500-c4410aa2', 86400 * 2, 2398, { pending_deletion_requested_at: ago(3600 * 6), pending_deletion_after: ahead(86400 - 3600 * 6), pending_deletion_by: 'alice' }),
    snap('bsd01.web', '20261018021500-b27d11f0', 86400 * 3, 2390),
    snap('bsd01.web', '20261017021500-a91c03de', 86400 * 4, 2380, { protected: true }),
    snap('bsd01.db', '20261021120000-44dd55ee', 3600, 15620, { verify_state: '', verified_at: undefined }),
    snap('bsd01.db', '20261021110000-33cc44dd', 3600 * 2, 15610, { verify_state: 'failed', verify_problems: ['chunk-corrupt 9f8a3c01d2e44b77', 'chunk-missing 03bd77aa11ee9902'] }),
    snap('bsd01.db', '20261021090000-22bb33cc', 3600 * 4, 15590),
    snap('bsd01.db', '20261021080000-11aa22bb', 3600 * 5, 15500, { protected: true }),
    snap('bsd02.mail', '20261021090000-66ff7700', 3600 * 4, 9800),
    snap('bsd02.mail', '20261021030000-55ee66ff', 3600 * 9, 9790),
    snap('bsd02.build', '20260920030000-aa55bb66', 86400 * 31, 3100, { pending_deletion_requested_at: ago(86400 * 4), pending_deletion_after: ago(86400 * 2), pending_deletion_by: 'bob' }),
    snap('bsd02.build', '20261008030000-88119922', 86400 * 13, 3180),
    snap('bsd02.build', '20261015030000-9922aa33', 86400 * 6, 3200),
  ]
}

export function sessions(): BackupSession[] {
  return [{ id: 's-77', group_id: 'bsd02.mail', host_id: 'h2', snapshot_id: '20261021130000-77aa88bb', state: 'open', created_at: ago(120), lease_expires_at: ahead(900), chunks_put: 410, bytes_put: 1.4 * GiB }]
}

export function jobs(): Job[] {
  return [
    { id: 14, kind: 'gc', state: 'succeeded', created_by: 'scheduler', created_at: ago(86400 * 2), started_at: ago(86400 * 2), finished_at: ago(86400 * 2 - 600), params: { grace: '36h' }, result: { scanned: 480000, live: 470000, kept_young: 2000, kept_pinned: 0, removed: 7800, bytes_reclaimed: 7.5 * GiB, chunk_objects: 472000, chunk_bytes: 96 * GiB, quarantine_purged: 0, orphans_removed: 0, snapshots_marked: 0, dry_run: false, grace: '36h' } },
    { id: 15, kind: 'verify', state: 'failed', created_by: 'scheduler', created_at: ago(86400 * 6), started_at: ago(86400 * 6), finished_at: ago(86400 * 6 - 3000), params: { mode: 'new', quarantine: true }, error: '2 snapshots failed verification', result: { mode: 'new', chunks_read: 482000, snapshots: 13, skipped: 0, failed: 1, quarantined: 2, problems: [{ kind: 'chunk-corrupt', object: '9f8a3c01d2e44b77', detail: 'digest mismatch' }, { kind: 'chunk-missing', object: '03bd77aa11ee9902', detail: 'not found' }] } },
    { id: 16, kind: 'retention', state: 'succeeded', created_by: 'scheduler', created_at: ago(86400), started_at: ago(86400), finished_at: ago(86400 - 30), result: { dry_run: false, deleted: 2, groups: [] } },
    { id: 13, kind: 'reconcile', state: 'succeeded', created_by: 'scheduler', created_at: ago(1500), started_at: ago(1500), finished_at: ago(1490) },
  ]
}

export function quarantine(): QuarantinedChunk[] {
  return [
    { id: '9f8a3c01d2e44b77a0b1c2d3e4f50617', size: 4 * MiB, since: ago(86400 * 6), healed: false },
    { id: '03bd77aa11ee9902ffc4d5e6a7b8c9d0', size: 2 * MiB, since: ago(86400 * 6), healed: false },
  ]
}

export function policies(): RetentionPolicyRow[] {
  return [
    { scope: 'default', policy: { last: 3, daily: 7, weekly: 4, monthly: 6 }, timezone: 'UTC', updated_at: ago(86400 * 20), updated_by: 'alice' },
    { scope: 'bsd01.db', policy: { hourly: 24, daily: 14 }, timezone: 'Europe/Berlin', updated_at: ago(86400 * 9), updated_by: 'alice' },
  ]
}

export function retentionPlan(dry: boolean, group?: string) {
  const mk = (g: string, snap: string, age: number, reasons: string[]) => ({ group: g, snap, time: ago(age), reasons })
  const del = [mk('bsd01.web', '20261018021500-b27d11f0', 86400 * 3, ['older than the 3 newest', 'not the last of its week']), mk('bsd02.build', '20261008030000-88119922', 86400 * 13, ['beyond 4 weekly'])]
    .filter((x) => !group || x.group === group)
  const byGroup = new Map<string, typeof del>()
  for (const d of del) byGroup.set(d.group, [...(byGroup.get(d.group) ?? []), d])
  return {
    dry_run: dry, deleted: dry ? 0 : del.length,
    groups: [...byGroup.entries()].map(([g, x]) => ({
      group: g, policy: { last: 3, daily: 7 }, policy_source: 'default' as const,
      keep: [{ snapshot: 'kept-1', time: ago(3600), reasons: ['last 3'] }],
      delete: x.map((d) => ({ snapshot: d.snap, time: d.time, reasons: d.reasons })),
    })),
  }
}

export function audit(): NodeAuditEntry[] {
  const a = (id: number, actor: string, action: string, target: string, age: number, detail?: unknown): NodeAuditEntry => ({ id, at: ago(age), actor, action, target, detail, remote: '203.0.113.5' })
  return [
    a(9, 'alice', 'snapshot.deletion.request', 'bsd01.web/20261019021500-c4410aa2', 3600 * 6, { delay: '24h' }),
    a(8, 'bsd01', 'session.commit', 'bsd01.web', 3600 * 9),
    a(7, 'alice', 'auth.login', '', 3600 * 10),
    a(6, 'bob', 'snapshot.deletion.request', 'bsd02.build/20260920030000-aa55bb66', 86400 * 4),
    a(5, 'anonymous', 'auth.login.failed', 'root', 86400 * 5),
    a(4, 'alice', 'maintenance.mode', 'read-only', 86400 * 8, { from: 'online' }),
  ]
}

export function users(): NodeUser[] {
  return [
    { name: 'alice', role: 'admin', disabled: false, created_at: ago(86400 * 60) },
    { name: 'bob', role: 'backup-admin', disabled: false, created_at: ago(86400 * 50) },
    { name: 'carol', role: 'readonly', disabled: true, created_at: ago(86400 * 40) },
  ]
}
export function tokens(): NodeToken[] {
  return [
    { id: 't1', name: 'ops-readonly', role: 'readonly', created_at: ago(86400 * 30), last_used_at: ago(3600) },
    { id: 't2', name: 'ci', role: 'backup-admin', allow_from: ['198.51.100.0/24'], expires_at: ahead(86400 * 30), created_at: ago(86400 * 10) },
  ]
}
