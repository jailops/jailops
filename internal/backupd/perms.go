package backupd

import (
	"jailops.net/jailops/internal/auth"
	"jailops.net/jailops/sdk/api"
)

// Roles of the management API. A user holds exactly one.
const (
	RoleReadonly    = "readonly"     // look at everything except users, tokens and the audit log
	RoleBackupAdmin = "backup-admin" // operate: GC, verify, retention, protection, deletion requests, maintenance
	RoleAdmin       = "admin"        // everything, including hosts, users, tokens and the audit log
)

var roleToPerm = map[string]api.Permission{
	RoleReadonly:    api.PermReadonly,
	RoleBackupAdmin: api.PermBackupAdmin,
	RoleAdmin:       api.PermAdmin,
}

func pub(method, pattern string) auth.Entry {
	return auth.Entry{Method: method, Pattern: pattern, Public: true}
}

func ent(method, pattern string, p api.Permission) auth.Entry {
	return auth.Entry{Method: method, Pattern: pattern, Perm: p}
}

const (
	ro = api.PermReadonly
	ba = api.PermBackupAdmin
	ad = api.PermAdmin
)

// Perms is the permission table of the management API of the backup node. It
// is separate from the table of the host daemon: the backup node has its own
// roles and its own routes. Every route the server registers must have an
// entry; the server refuses to start otherwise. The routes of the host
// listener and of the admin socket are node.HostRoutes and node.AdminRoutes.
var Perms = []auth.Entry{
	pub("GET", "/1.0"),
	pub("POST", "/1.0/auth/login"),
	ent("POST", "/1.0/auth/logout", ro),
	ent("GET", "/1.0/auth/session", ro),
	ent("POST", "/1.0/auth/password", ro),

	ent("GET", "/1.0/repository", ro),
	ent("GET", "/1.0/certificate", ro),

	ent("GET", "/1.0/hosts", ro),
	ent("POST", "/1.0/hosts", ad),
	ent("PUT", "/1.0/hosts/{name}", ad),
	ent("DELETE", "/1.0/hosts/{name}", ad),

	ent("GET", "/1.0/groups", ro),
	ent("GET", "/1.0/groups/{group}/snapshots", ro),
	ent("GET", "/1.0/groups/{group}/snapshots/{snap}", ro),
	ent("PUT", "/1.0/groups/{group}/snapshots/{snap}/protected", ba),
	ent("POST", "/1.0/groups/{group}/snapshots/{snap}/deletion", ba),
	ent("DELETE", "/1.0/groups/{group}/snapshots/{snap}/deletion", ba),
	ent("POST", "/1.0/groups/{group}/snapshots/{snap}/deletion/execute", ba),

	ent("GET", "/1.0/sessions", ro),
	ent("GET", "/1.0/jobs", ro),
	ent("GET", "/1.0/jobs/{id}", ro),
	ent("POST", "/1.0/jobs/{id}/cancel", ba),
	ent("POST", "/1.0/jobs/gc", ba),
	ent("POST", "/1.0/jobs/verify", ba),
	ent("POST", "/1.0/jobs/retention", ba),
	ent("POST", "/1.0/jobs/reconcile", ba),

	ent("GET", "/1.0/maintenance", ro),
	ent("PUT", "/1.0/maintenance", ba),
	ent("GET", "/1.0/quarantine", ro),
	ent("POST", "/1.0/quarantine/purge", ba),
	ent("GET", "/1.0/retention-policies", ro),
	ent("PUT", "/1.0/retention-policies/{scope}", ba),
	ent("DELETE", "/1.0/retention-policies/{scope}", ba),

	ent("GET", "/1.0/audit", ad),
	ent("GET", "/1.0/users", ad),
	ent("POST", "/1.0/users", ad),
	ent("PUT", "/1.0/users/{name}", ad),
	ent("DELETE", "/1.0/users/{name}", ad),
	ent("GET", "/1.0/tokens", ad),
	ent("POST", "/1.0/tokens", ad),
	ent("DELETE", "/1.0/tokens/{name}", ad),
}

// Registry is the registry over Perms.
func Registry() (*auth.Registry, error) { return auth.NewRegistry(Perms) }
