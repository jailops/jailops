// Every path the backup console calls, in one module: the management API of jailops-backupd under /1.0,
// as documented in internal/backupd/api.go. A test checks the first segments against web/apps.json.
import { getClient } from '@jailops/shared/api/client'
import { core } from '@jailops/shared/api/core'
import type {
  Repository, Host, HostBody, Group, Snapshot, Deletion, BackupSession, Job, JobKind, QuarantinedChunk, RetentionPolicyRow, NodeAuditEntry,
  NodeUser, UserBody, NodeToken, TokenBody, Mode,
} from '../types'
import type { RetentionPolicy } from '@jailops/shared/api/types'

const e = encodeURIComponent
const c = () => getClient()
const snap = (g: string, s: string) => `/groups/${e(g)}/snapshots/${e(s)}`

export const api = {
  ...core,
  repository: () => c().get<Repository>('/repository'),
  /** The node's certificate fingerprint, which hosts pin. */
  certificate: () => c().get<{ fingerprint: string }>('/certificate'),

  hosts: {
    list: () => c().list<Host>('/hosts'),
    enrol: (b: HostBody) => c().mutate<Host>('POST', '/hosts', b),
    setScopes: (name: string, scopes: string[]) => c().mutate('PUT', `/hosts/${e(name)}`, { scopes }),
    revoke: (name: string) => c().mutate('DELETE', `/hosts/${e(name)}`),
  },

  groups: {
    list: () => c().list<Group>('/groups'),
    snapshots: (g: string) => c().list<Snapshot>(`/groups/${e(g)}/snapshots`),
    setProtected: (g: string, s: string, protectedFlag: boolean) => c().mutate('PUT', `${snap(g, s)}/protected`, { protected: protectedFlag }),
    requestDeletion: (g: string, s: string, delay?: string) => c().mutate<Deletion>('POST', `${snap(g, s)}/deletion`, delay ? { delay } : {}),
    cancelDeletion: (g: string, s: string) => c().mutate('DELETE', `${snap(g, s)}/deletion`),
    executeDeletion: (g: string, s: string) => c().mutate('POST', `${snap(g, s)}/deletion/execute`),
  },

  sessions: { list: () => c().list<BackupSession>('/sessions') },

  jobs: {
    list: (kind?: JobKind, limit = 50) => c().list<Job>(`/jobs?${kind ? `kind=${e(kind)}&` : ''}limit=${limit}`),
    get: (id: number) => c().get<Job>(`/jobs/${id}`),
    cancel: (id: number) => c().mutate('POST', `/jobs/${id}/cancel`),
    gc: (b: { grace?: string; dry_run?: boolean }) => c().mutate<Job>('POST', '/jobs/gc', b),
    verify: (b: { mode: 'all' | 'new' | 'stale'; quarantine?: boolean; stale_days?: number }) => c().mutate<Job>('POST', '/jobs/verify', b),
    retention: (b: { dry_run?: boolean; group?: string }) => c().mutate<Job>('POST', '/jobs/retention', b),
    reconcile: () => c().mutate<Job>('POST', '/jobs/reconcile'),
  },

  maintenance: {
    get: () => c().get<{ mode: Mode }>('/maintenance'),
    set: (mode: Mode) => c().mutate<{ mode: Mode }>('PUT', '/maintenance', { mode }),
  },
  quarantine: {
    list: () => c().list<QuarantinedChunk>('/quarantine'),
    purge: () => c().mutate<{ removed: number }>('POST', '/quarantine/purge'),
  },
  retentionPolicies: {
    list: () => c().list<RetentionPolicyRow>('/retention-policies'),
    set: (scope: string, policy: RetentionPolicy, timezone?: string) => c().mutate('PUT', `/retention-policies/${e(scope)}`, { policy, ...(timezone ? { timezone } : {}) }),
    remove: (scope: string) => c().mutate('DELETE', `/retention-policies/${e(scope)}`),
  },

  audit: (o: { action?: string; limit?: number; before?: number } = {}) =>
    c().list<NodeAuditEntry>(`/audit?limit=${o.limit ?? 100}${o.action ? `&action=${e(o.action)}` : ''}${o.before ? `&before=${o.before}` : ''}`),
  users: {
    list: () => c().list<NodeUser>('/users'),
    create: (b: UserBody) => c().mutate('POST', '/users', b),
    update: (name: string, b: UserBody) => c().mutate('PUT', `/users/${e(name)}`, b),
    remove: (name: string) => c().mutate('DELETE', `/users/${e(name)}`),
  },
  tokens: {
    list: () => c().list<NodeToken>('/tokens'),
    create: (b: TokenBody) => c().mutate<NodeToken & { secret: string }>('POST', '/tokens', b),
    remove: (name: string) => c().mutate('DELETE', `/tokens/${e(name)}`),
  },
}
