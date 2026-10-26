// Shapes of the backup node's management API (internal/backupd/api.go). The envelope is api.Response;
// these are the `metadata` payloads. Keep in step with that file.
export * from '@jailops/shared/api/types'
import type { RetentionPolicy, RepositoryStatus as ApiRepositoryStatus } from '@jailops/shared/api/types'

export type NodeRole = 'readonly' | 'backup-admin' | 'admin'
export type Mode = 'online' | 'read-only' | 'offline'

/** GET /repository: api.RepositoryStatus fields plus the counters of the node, `status` and `next_runs`. */
export interface Repository extends ApiRepositoryStatus {
  groups: number
  failed_snapshots: number
  protected_snapshots: number
  pending_deletions: number
  status: ApiRepositoryStatus
  /** Next scheduled run of each job kind. */
  next_runs: Record<string, string>
}

export interface Host {
  id: string
  name: string
  fingerprint: string
  scopes: string[]
  note?: string
  source: string
  created_at: string
  revoked_at?: string
  last_seen_at?: string
}
export interface HostBody { name: string; fingerprint?: string; certificate?: string; scopes?: string[]; note?: string }

/** GET /groups: one row per jail group. */
export interface Group {
  group_id: string
  host_id?: string
  host_name?: string
  created_at: string
  last_activity?: string
  snapshots: number
  failed: number
  protected: number
  pending_deletion: number
  latest?: string
}

export interface Snapshot {
  group_id: string
  snapshot_id: string
  committed_at: string
  chunk_count: number
  index_bytes: number
  source: string
  protected: boolean
  verify_state: '' | 'ok' | 'failed'
  verified_at?: string
  verify_problems?: string[]
  pending_deletion_requested_at?: string
  pending_deletion_after?: string
  pending_deletion_by?: string
}

export interface Deletion { requested_at: string; execute_after: string; requested_by: string }

export interface BackupSession {
  id: string
  group_id: string
  host_id: string
  snapshot_id: string
  state: string
  created_at: string
  lease_expires_at: string
  closed_at?: string
  chunks_put: number
  bytes_put: number
}

export type JobKind = 'gc' | 'verify' | 'retention' | 'reconcile' | 'restore-test'
export type JobState = 'queued' | 'running' | 'succeeded' | 'failed' | 'cancelled'
export interface Job<R = unknown> {
  id: number
  kind: JobKind
  state: JobState
  params?: Record<string, unknown>
  result?: R
  error?: string
  created_by?: string
  created_at: string
  started_at?: string
  finished_at?: string
}
export interface GCResult {
  scanned: number; live: number; kept_young: number; kept_pinned: number; removed: number; bytes_reclaimed: number
  chunk_objects: number; chunk_bytes: number; quarantine_purged: number; orphans_removed: number; snapshots_marked: number
  dry_run: boolean; grace: string
}
export interface VerifyProblem { kind: string; object: string; detail: string }
export interface VerifyResult {
  mode: string; chunks_read: number; snapshots: number; skipped: number; failed: number; quarantined: number
  problems?: VerifyProblem[]
  results?: Array<{ group: string; snapshot: string; state: string; chunks: number; problems?: string[] }>
}
export interface RetentionDecision { snapshot: string; time: string; reasons: string[] }
export interface RetentionGroup {
  group: string
  policy: RetentionPolicy
  policy_source: 'group' | 'default' | 'config' | 'none'
  keep: RetentionDecision[]
  delete: RetentionDecision[]
  deleted?: string[]
  skipped?: string[]
  error?: string
}
export interface RetentionResult { dry_run: boolean; groups: RetentionGroup[]; deleted: number }

export interface QuarantinedChunk { id: string; size: number; since: string; healed: boolean }
export interface RetentionPolicyRow { scope: string; policy: RetentionPolicy; timezone: string; updated_at: string; updated_by: string }

export interface NodeAuditEntry { id: number; at: string; actor: string; action: string; target?: string; detail?: unknown; remote?: string }
export interface NodeUser { name: string; role: NodeRole; disabled: boolean; created_at: string }
export interface UserBody { name?: string; password?: string; role?: NodeRole; disabled?: boolean }
export interface NodeToken { id: string; name: string; role: NodeRole; allow_from?: string[]; expires_at?: string; created_at: string; last_used_at?: string }
export interface TokenBody { name: string; role: NodeRole; allow_from?: string[]; expires_at?: string }
