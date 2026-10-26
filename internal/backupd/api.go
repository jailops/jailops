package backupd

// The management API of the backup node, for its web UI and for scripts. It is
// served on web.listen (TLS, no client certificate) under /1.0 next to the UI
// files, and every answer is the envelope of sdk/api (api.Response): the payload
// is in "metadata", an error in "error" with a stable code.
//
// Authentication: POST /1.0/auth/login {username, password} sets a session cookie
// and returns {user, role, csrf}; browser sessions send the csrf value in
// X-CSRF-Token on every request that is not GET. Scripts send
// "Authorization: Bearer jot_...". Roles: readonly < backup-admin < admin.
//
//	GET    /1.0                                  server info, api.ServerInfo with role "backup" (public)
//	POST   /1.0/auth/login                       public; 429 rate_limited after repeated failures
//	POST   /1.0/auth/logout | GET /1.0/auth/session | POST /1.0/auth/password {current, new}
//	GET    /1.0/repository                       readonly. node.RepositoryStatus plus "status" (api.RepositoryStatus) and "next_runs"
//	GET    /1.0/certificate                      readonly. {fingerprint}: the node's certificate, which hosts pin
//	GET    /1.0/hosts                            readonly. []ctl.Host {id, name, fingerprint, scopes, note, created_at, revoked_at, last_seen_at}
//	POST   /1.0/hosts                            admin. {name, fingerprint | certificate (PEM), scopes?, note?}
//	PUT    /1.0/hosts/{name}                     admin. {scopes}
//	DELETE /1.0/hosts/{name}                     admin. revokes; open sessions are aborted
//	GET    /1.0/groups                           readonly. one row per jail group with snapshot, failed, protected and pending counts
//	GET    /1.0/groups/{group}/snapshots         readonly. []wire.Snapshot {snapshot_id, committed_at, chunk_count, protected,
//	                                             verify_state "", "ok" or "failed", verified_at, verify_problems,
//	                                             pending_deletion_requested_at/_after/_by}
//	GET    /1.0/groups/{group}/snapshots/{snap}  readonly
//	PUT    /1.0/groups/{group}/snapshots/{snap}/protected         backup-admin. {protected: bool}
//	POST   /1.0/groups/{group}/snapshots/{snap}/deletion          backup-admin. {delay?: "72h"} step one; returns {requested_at, execute_after, requested_by}
//	DELETE /1.0/groups/{group}/snapshots/{snap}/deletion          backup-admin. cancel
//	POST   /1.0/groups/{group}/snapshots/{snap}/deletion/execute  backup-admin. step two; 409 busy before execute_after
//	GET    /1.0/sessions                         readonly. open backup sessions (leases)
//	GET    /1.0/jobs?kind=&limit=                readonly. ctl.Job {id, kind gc|verify|retention|reconcile|restore-test, state, params, result, error, ...}
//	GET    /1.0/jobs/{id}                        readonly
//	POST   /1.0/jobs/{id}/cancel                 backup-admin
//	POST   /1.0/jobs/gc {grace?, dry_run?}       backup-admin. 202 with the job; 409 busy if another repository-wide job runs
//	POST   /1.0/jobs/verify {mode: all|new|stale, quarantine?, stale_days?}
//	POST   /1.0/jobs/retention {dry_run?, group?}  the plan is in the job's result
//	POST   /1.0/jobs/reconcile
//	GET    /1.0/maintenance                      readonly. {mode: online|read-only|offline}
//	PUT    /1.0/maintenance {mode}               backup-admin. hosts get 503 "maintenance" while it is not online
//	GET    /1.0/quarantine                       readonly. []node.QuarantinedChunk {id, size, since, healed}
//	POST   /1.0/quarantine/purge                 backup-admin
//	GET    /1.0/retention-policies               readonly. {scope: "default" | group id, policy, timezone}
//	PUT    /1.0/retention-policies/{scope}       backup-admin. {policy: api.RetentionPolicy, timezone?}
//	DELETE /1.0/retention-policies/{scope}       backup-admin
//	GET    /1.0/audit?action=&limit=&before=     admin. []ctl.AuditEntry, newest first
//	GET|POST /1.0/users, PUT|DELETE /1.0/users/{name}   admin. {name, password, role, disabled}
//	GET|POST /1.0/tokens, DELETE /1.0/tokens/{name}     admin. POST {name, role, allow_from?, expires_at?} returns the secret once
//
// Error codes the UI should handle: unauthorized, forbidden, not_found, invalid, already_exists, busy
// (another job or a protected snapshot), rate_limited, maintenance.
//
// The routes are listed with their permissions in Perms (perms.go).
