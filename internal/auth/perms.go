// Package auth holds everything that decides who may do what: the route
// permission table, API tokens, password hashing, TOTP, sessions and CSRF,
// login lockout, certificate pinning for mTLS, join tokens and the unix socket
// peer credentials (which the helper uses to decide who may connect; jailopsd recognises only uid 0 on its socket). Secrets are never logged or returned from here except
// where a function is documented as returning a freshly generated secret.
package auth

import (
	"fmt"
	"sort"
	"strings"

	"jailops.net/jailops/sdk/api"
)

// Entry is one row of the permission table. Pattern uses net/http ServeMux
// syntax without the method: literal segments, "{name}" for one segment and
// "{name...}" for the rest of the path.
type Entry struct {
	Method  string
	Pattern string
	Perm    api.Permission
	// Public routes need no authentication (login, server info).
	Public bool
	// Class says how the route is scoped to projects (see Class).
	Class Class
}

// Class says what an identity's project scope means for a route. The
// authorization tests derive their expectations from it. The role check
// (Entry.Perm) always comes first and is forbidden when it fails.
//
//   - ClassPublic: no authentication.
//   - ClassSelf: any authenticated identity whose role satisfies the entry,
//     even one with neither projects nor global (/1.0/auth/*, sessions).
//   - ClassGlobal: host-level; needs a global identity. Project-restricted
//     and scope-less identities are forbidden.
//   - ClassGranted: host-level objects whose reads project identities may
//     do (answers filtered by grants, membership or allow-lists; not_found
//     for what is not granted); writes need a global admin (forbidden).
//   - ClassProject: the object lives in the project named by ?project=
//     (default "default"). A scope-less identity is forbidden; an
//     inaccessible or unknown project is not_found.
//   - ClassVisible: a list or stream filtered by project; scope never
//     causes an error, except for a scope-less identity (forbidden).
//   - ClassObject: one object whose project is looked up; not_found when it
//     is invisible to the caller.
type Class string

// Route classes.
const (
	ClassPublic  Class = "public"
	ClassSelf    Class = "self"
	ClassGlobal  Class = "global"
	ClassGranted Class = "granted"
	ClassProject Class = "project"
	ClassVisible Class = "visible"
	ClassObject  Class = "object"
)

func (c Class) valid() bool {
	switch c {
	case ClassPublic, ClassSelf, ClassGlobal, ClassGranted, ClassProject, ClassVisible, ClassObject:
		return true
	}
	return false
}

// classify returns the class of an entry from its route.
func classify(e Entry) Class {
	p := e.Pattern
	has := func(prefix string) bool { return p == prefix || strings.HasPrefix(p, prefix+"/") }
	switch {
	case e.Public:
		return ClassPublic
	case has("/1.0/auth"), has("/1.0/sessions"), p == "/1.0/config-keys":
		return ClassSelf
	case p == "/1.0/events", p == "/1.0/audit", p == "/1.0/updates":
		return ClassVisible
	case p == "/1.0/updates/check", p == "/1.0/updates/apply":
		return ClassProject
	case p == "/1.0/operations":
		return ClassVisible
	case has("/1.0/operations"):
		return ClassObject
	case p == "/1.0/backups":
		if e.Method == "GET" {
			return ClassVisible
		}
		return ClassProject
	case p == "/1.0/backups/{id}":
		return ClassObject
	case p == "/1.0/backups/run", has("/1.0/backup"):
		return ClassGlobal
	case has("/1.0/backups"), has("/1.0/restores"):
		return ClassProject
	case has("/1.0/images"), has("/1.0/networks"), has("/1.0/storage"), has("/1.0/projects"):
		return ClassGranted
	case has("/1.0/jails"), has("/1.0/profiles"), has("/1.0/firewall"), has("/1.0/backup-policies"), has("/1.0/jail-templates"):
		return ClassProject
	case has("/1.0/trust"), has("/1.0/users"), has("/1.0/tokens"), has("/1.0/certificates"), has("/1.0/remotes"),
		has("/1.0/groups"), has("/1.0/rollouts"), has("/1.0/cluster"), has("/1.0/server"), has("/1.0/doctor"),
		has("/1.0/resources"), has("/1.0/backup-targets"):
		return ClassGlobal
	}
	panic("auth: no route class for " + e.Key())
}

// Key is the "METHOD pattern" string the HTTP layer registers.
func (e Entry) Key() string { return e.Method + " " + e.Pattern }

func r(method, pattern string, p api.Permission) Entry {
	return Entry{Method: method, Pattern: pattern, Perm: p}
}
func pub(method, pattern string) Entry { return Entry{Method: method, Pattern: pattern, Public: true} }

const (
	ro = api.PermReadonly
	op = api.PermOperator
	ad = api.PermAdmin
)

// Perms is the permission table. Every route of the HTTP layer must have an
// entry; the daemon refuses to start otherwise (VerifyComplete).
//
// Rules of thumb: reads are readonly except for secrets-adjacent resources
// (users, tokens, certificates, audit) which are admin; lifecycle, snapshots
// and exec are operator; configuration is admin; backup transport uses the
// narrow roles.
var Perms = classifyAll([]Entry{
	// Server and authentication.
	pub("GET", "/1.0"),
	pub("POST", "/1.0/auth/login"),
	r("POST", "/1.0/auth/logout", ro),
	r("GET", "/1.0/auth/session", ro),
	r("POST", "/1.0/auth/password", ro),
	r("POST", "/1.0/auth/totp", ro),
	r("DELETE", "/1.0/auth/totp", ro),
	r("POST", "/1.0/auth/recovery-codes", ro),
	pub("POST", "/1.0/cluster/join"),
	r("GET", "/1.0/cluster", ro),
	r("POST", "/1.0/cluster/tokens", ad),
	r("PUT", "/1.0/cluster", ad),
	r("DELETE", "/1.0/cluster/members/{name}", ad),
	r("GET", "/1.0/cluster/members", ro),
	r("GET", "/1.0/events", ro),
	r("GET", "/1.0/audit", ad),
	r("POST", "/1.0/trust/tokens", ad),
	pub("POST", "/1.0/trust/clients"),
	r("GET", "/1.0/sessions", ro),
	r("DELETE", "/1.0/sessions/{id}", ro),
	r("GET", "/1.0/doctor", ro),
	r("GET", "/1.0/resources", ro),
	r("PUT", "/1.0/server", ad),
	r("GET", "/1.0/server/config", ro),

	// Operations.
	r("GET", "/1.0/operations", ro),
	r("GET", "/1.0/operations/{id}", ro),
	r("GET", "/1.0/operations/{id}/wait", ro),
	r("DELETE", "/1.0/operations/{id}", op),

	// Configuration keys and profiles.
	r("GET", "/1.0/config-keys", ro),
	r("POST", "/1.0/jails/{name}/config", op),
	r("GET", "/1.0/updates", ro),
	r("POST", "/1.0/updates/check", op),
	r("POST", "/1.0/updates/apply", op),
	r("GET", "/1.0/jails/{name}/updates", ro),
	r("POST", "/1.0/jails/{name}/provision", ad),
	r("GET", "/1.0/profiles", ro),
	r("POST", "/1.0/profiles", ad),
	r("GET", "/1.0/profiles/{name}", ro),
	r("PUT", "/1.0/profiles/{name}", ad),
	r("PATCH", "/1.0/profiles/{name}", ad),
	r("DELETE", "/1.0/profiles/{name}", ad),

	// Projects and storage.
	r("GET", "/1.0/projects", ro),
	r("POST", "/1.0/projects", ad),
	r("GET", "/1.0/projects/{name}", ro),
	r("PUT", "/1.0/projects/{name}", ad),
	r("PATCH", "/1.0/projects/{name}", ad),
	r("DELETE", "/1.0/projects/{name}", ad),
	r("GET", "/1.0/storage", ro),
	r("POST", "/1.0/storage", ad),
	r("GET", "/1.0/storage/{name}", ro),
	r("PUT", "/1.0/storage/{name}", ad),
	r("PATCH", "/1.0/storage/{name}", ad),
	r("DELETE", "/1.0/storage/{name}", ad),

	// Images and generations.
	r("GET", "/1.0/images", ro),
	r("POST", "/1.0/images", ad),
	r("GET", "/1.0/images/{name}", ro),
	r("PUT", "/1.0/images/{name}", ad),
	r("PATCH", "/1.0/images/{name}", ad),
	r("DELETE", "/1.0/images/{name}", ad),
	r("GET", "/1.0/images/{name}/generations", ro),
	r("POST", "/1.0/images/{name}/generations", ad),
	r("GET", "/1.0/images/{name}/generations/{gen}", ro),
	r("DELETE", "/1.0/images/{name}/generations/{gen}", ad),
	r("PUT", "/1.0/images/{name}/aliases/{alias}", ad),
	r("DELETE", "/1.0/images/{name}/aliases/{alias}", ad),

	// Jails.
	r("GET", "/1.0/jails", ro),
	r("POST", "/1.0/jails", ad),
	r("GET", "/1.0/jails/{name}", ro),
	r("PUT", "/1.0/jails/{name}", ad),
	r("PATCH", "/1.0/jails/{name}", ad),
	r("DELETE", "/1.0/jails/{name}", ad),
	r("POST", "/1.0/jails/{name}", ad), // rename, copy
	r("GET", "/1.0/jails/{name}/state", ro),
	r("PUT", "/1.0/jails/{name}/state", op),
	r("POST", "/1.0/jails/{name}/exec", op),
	r("GET", "/1.0/jails/{name}/console", op),
	r("POST", "/1.0/jails/{name}/console", op),
	r("GET", "/1.0/jails/{name}/console/{id}/output", op),
	r("POST", "/1.0/jails/{name}/console/{id}/input", op),
	r("POST", "/1.0/jails/{name}/console/{id}/resize", op),
	r("DELETE", "/1.0/jails/{name}/console/{id}", op),
	r("GET", "/1.0/jails/{name}/logs", ro),
	r("GET", "/1.0/jails/{name}/files/{path...}", op),
	r("PUT", "/1.0/jails/{name}/files/{path...}", op),
	r("POST", "/1.0/jails/{name}/upgrade", op),
	r("POST", "/1.0/jails/{name}/rollback", op),
	r("POST", "/1.0/jails/{name}/move", ad),
	r("GET", "/1.0/jails/{name}/snapshots", ro),
	r("POST", "/1.0/jails/{name}/snapshots", op),
	r("GET", "/1.0/jails/{name}/snapshots/{snapshot}", ro),
	r("DELETE", "/1.0/jails/{name}/snapshots/{snapshot}", op),
	r("POST", "/1.0/jails/{name}/snapshots/{snapshot}", op), // rollback, rename
	r("GET", "/1.0/jails/{name}/volumes", ro),
	r("POST", "/1.0/jails/{name}/volumes", ad),
	r("DELETE", "/1.0/jails/{name}/volumes/{volume}", ad),
	r("GET", "/1.0/jails/{name}/bundle", ro),
	r("GET", "/1.0/jail-templates", ro),
	r("POST", "/1.0/jail-templates", ad),
	r("GET", "/1.0/jail-templates/{name}", ro),
	r("PUT", "/1.0/jail-templates/{name}", ad),
	r("DELETE", "/1.0/jail-templates/{name}", ad),

	// Networks and firewall.
	r("GET", "/1.0/networks", ro),
	r("POST", "/1.0/networks", ad),
	r("GET", "/1.0/networks/{name}", ro),
	r("PUT", "/1.0/networks/{name}", ad),
	r("PATCH", "/1.0/networks/{name}", ad),
	r("DELETE", "/1.0/networks/{name}", ad),
	r("GET", "/1.0/networks/{name}/leases", ro),
	r("GET", "/1.0/firewall/policies", ro),
	r("POST", "/1.0/firewall/policies", ad),
	r("GET", "/1.0/firewall/policies/{name}", ro),
	r("PUT", "/1.0/firewall/policies/{name}", ad),
	r("PATCH", "/1.0/firewall/policies/{name}", ad),
	r("DELETE", "/1.0/firewall/policies/{name}", ad),
	r("GET", "/1.0/firewall/address-sets", ro),
	r("POST", "/1.0/firewall/address-sets", ad),
	r("GET", "/1.0/firewall/address-sets/{name}", ro),
	r("PUT", "/1.0/firewall/address-sets/{name}", ad),
	r("PATCH", "/1.0/firewall/address-sets/{name}", ad),
	r("DELETE", "/1.0/firewall/address-sets/{name}", ad),

	// Backup. Transport uses the narrow roles (exact match). Only admin also satisfies restore; admin does not satisfy
	// backup-admin (deleting a backup), backup-push or backup-pull.
	r("GET", "/1.0/backup-targets", ro),
	r("POST", "/1.0/backup-targets", ad),
	r("GET", "/1.0/backup-targets/{name}", ro),
	r("PUT", "/1.0/backup-targets/{name}", ad),
	r("PATCH", "/1.0/backup-targets/{name}", ad),
	r("DELETE", "/1.0/backup-targets/{name}", ad),
	r("GET", "/1.0/backup-policies", ro),
	r("POST", "/1.0/backup-policies", ad),
	r("GET", "/1.0/backup-policies/{name}", ro),
	r("PUT", "/1.0/backup-policies/{name}", ad),
	r("PATCH", "/1.0/backup-policies/{name}", ad),
	r("DELETE", "/1.0/backup-policies/{name}", ad),
	r("GET", "/1.0/backups", ro),
	r("POST", "/1.0/backups", op),
	r("GET", "/1.0/backups/{id}", ro),
	r("DELETE", "/1.0/backups/{id}", api.PermBackupAdmin),
	r("POST", "/1.0/backups/{id}/restore-preflight", api.PermRestore),
	r("POST", "/1.0/backups/{id}/restore", api.PermRestore),
	r("POST", "/1.0/restores", api.PermRestore),
	r("GET", "/1.0/backup/key", ad),
	r("POST", "/1.0/backup/key", ad),
	r("POST", "/1.0/backup/key/escrow", ad),
	r("POST", "/1.0/backups/run", ad),
	r("POST", "/1.0/backups/{id}/verify", op),
	r("POST", "/1.0/backups/receive", api.PermBackupPush),
	r("GET", "/1.0/backups/{id}/stream", api.PermBackupPull),
	r("GET", "/1.0/backups/chains", api.PermBackupPull),

	// Hub: remotes, groups, rollouts.
	r("GET", "/1.0/remotes", ro),
	r("POST", "/1.0/remotes", ad),
	r("GET", "/1.0/remotes/{name}", ro),
	r("PUT", "/1.0/remotes/{name}", ad),
	r("PATCH", "/1.0/remotes/{name}", ad),
	r("DELETE", "/1.0/remotes/{name}", ad),
	r("GET", "/1.0/groups", ro),
	r("POST", "/1.0/groups", ad),
	r("GET", "/1.0/groups/{name}", ro),
	r("PUT", "/1.0/groups/{name}", ad),
	r("DELETE", "/1.0/groups/{name}", ad),
	r("GET", "/1.0/rollouts", ro),
	r("POST", "/1.0/rollouts", ad),
	r("GET", "/1.0/rollouts/{name}", ro),
	r("PUT", "/1.0/rollouts/{name}", ad),
	r("DELETE", "/1.0/rollouts/{name}", ad),

	// Identity: users, tokens, certificates.
	r("GET", "/1.0/users", ad),
	r("POST", "/1.0/users", ad),
	r("GET", "/1.0/users/{name}", ad),
	r("PUT", "/1.0/users/{name}", ad),
	r("PATCH", "/1.0/users/{name}", ad),
	r("DELETE", "/1.0/users/{name}", ad),
	r("POST", "/1.0/users/{name}/password", ad),
	r("GET", "/1.0/tokens", ad),
	r("POST", "/1.0/tokens", ad),
	r("GET", "/1.0/tokens/{name}", ad),
	r("DELETE", "/1.0/tokens/{name}", ad),
	r("GET", "/1.0/certificates", ad),
	r("POST", "/1.0/certificates", ad),
	r("GET", "/1.0/certificates/{fingerprint}", ad),
	r("PUT", "/1.0/certificates/{fingerprint}", ad),
	r("PATCH", "/1.0/certificates/{fingerprint}", ad),
	r("DELETE", "/1.0/certificates/{fingerprint}", ad),
})

func classifyAll(es []Entry) []Entry {
	for i := range es {
		if es[i].Class == "" {
			es[i].Class = classify(es[i])
		}
	}
	return es
}

// Registry answers permission questions from a table and checks that the
// HTTP layer's route list is covered by it.
type Registry struct {
	byKey   map[string]Entry
	byShape map[string][]Entry // method -> entries
}

// NewRegistry builds a registry. It rejects malformed or duplicate entries.
func NewRegistry(entries []Entry) (*Registry, error) {
	reg := &Registry{byKey: map[string]Entry{}, byShape: map[string][]Entry{}}
	for _, e := range entries {
		if !validMethod(e.Method) || !strings.HasPrefix(e.Pattern, "/") {
			return nil, fmt.Errorf("auth: malformed permission entry %q", e.Key())
		}
		if e.Class == "" {
			// A table kept outside this package (the backup node's) has no
			// project scoping; its routes are host-level.
			e.Class = ClassGlobal
			if e.Public {
				e.Class = ClassPublic
			}
		}
		if !e.Class.valid() {
			return nil, fmt.Errorf("auth: entry %q has an unknown route class %q", e.Key(), e.Class)
		}
		if (e.Class == ClassPublic) != e.Public {
			return nil, fmt.Errorf("auth: entry %q: only public entries have class public", e.Key())
		}
		if !e.Public && e.Perm == "" {
			return nil, fmt.Errorf("auth: entry %q has no permission and is not public", e.Key())
		}
		if err := checkPattern(e.Pattern); err != nil {
			return nil, fmt.Errorf("auth: entry %q: %w", e.Key(), err)
		}
		if _, dup := reg.byKey[e.Key()]; dup {
			return nil, fmt.Errorf("auth: duplicate permission entry %q", e.Key())
		}
		reg.byKey[e.Key()] = e
		reg.byShape[e.Method] = append(reg.byShape[e.Method], e)
	}
	return reg, nil
}

// DefaultRegistry is the registry over Perms.
func DefaultRegistry() *Registry {
	reg, err := NewRegistry(Perms)
	if err != nil {
		panic(err) // a malformed static table is a programming error caught by tests
	}
	return reg
}

func validMethod(m string) bool {
	switch m {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
		return true
	}
	return false
}

func checkPattern(p string) error {
	segs := strings.Split(strings.TrimPrefix(p, "/"), "/")
	for i, s := range segs {
		switch {
		case s == "":
			return fmt.Errorf("empty path segment")
		case strings.HasPrefix(s, "{"):
			if !strings.HasSuffix(s, "}") || len(s) < 3 {
				return fmt.Errorf("bad wildcard %q", s)
			}
			if strings.HasSuffix(s, "...}") && i != len(segs)-1 {
				return fmt.Errorf("multi-segment wildcard must be last")
			}
		case strings.ContainsAny(s, "{}"):
			return fmt.Errorf("wildcard must be a whole segment: %q", s)
		}
	}
	return nil
}

// Lookup returns the entry registered for exactly this method and pattern.
func (g *Registry) Lookup(method, pattern string) (Entry, bool) {
	e, ok := g.byKey[method+" "+pattern]
	return e, ok
}

// Class returns the route class of an entry registered for exactly this
// method and pattern.
func (g *Registry) Class(method, pattern string) (Class, bool) {
	e, ok := g.Lookup(method, pattern)
	return e.Class, ok
}

// Match finds the most specific entry whose pattern matches a concrete path.
func (g *Registry) Match(method, path string) (Entry, bool) {
	var best Entry
	bestScore, found := -1, false
	for _, e := range g.byShape[method] {
		if score, ok := matchScore(e.Pattern, path); ok && score > bestScore {
			best, bestScore, found = e, score, true
		}
	}
	return best, found
}

// matchScore returns the number of literal segments matched (higher is more specific).
func matchScore(pattern, path string) (int, bool) {
	ps := strings.Split(strings.TrimPrefix(pattern, "/"), "/")
	xs := strings.Split(strings.TrimPrefix(path, "/"), "/")
	score := 0
	for i, p := range ps {
		if strings.HasPrefix(p, "{") && strings.HasSuffix(p, "...}") {
			return score, len(xs) > i && xs[i] != ""
		}
		if i >= len(xs) || xs[i] == "" {
			return 0, false
		}
		if strings.HasPrefix(p, "{") {
			continue
		}
		if p != xs[i] {
			return 0, false
		}
		score += 2
	}
	return score, len(ps) == len(xs)
}

// VerifyComplete fails if any route ("METHOD pattern") lacks an entry. The
// daemon calls it at start and refuses to run on error. Routes are listed in
// the error, sorted.
func (g *Registry) VerifyComplete(routes []string) error {
	var missing []string
	for _, rt := range routes {
		if _, ok := g.byKey[rt]; !ok {
			missing = append(missing, rt)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return fmt.Errorf("auth: %d route(s) have no entry in the permission table: %s", len(missing), strings.Join(missing, "; "))
}

// VerifyComplete checks routes against the default table.
func VerifyComplete(routes []string) error { return DefaultRegistry().VerifyComplete(routes) }

// Authorizer decides whether a holder of a permission may call a route.
type Authorizer struct{ Reg *Registry }

// NewAuthorizer returns an authorizer over the default table.
func NewAuthorizer() *Authorizer { return &Authorizer{Reg: DefaultRegistry()} }

// Check authorises held for the registered route (method, pattern). A route
// without an entry is denied: an unlisted route is never open.
func (a *Authorizer) Check(held api.Permission, method, pattern string) error {
	e, ok := a.Reg.Lookup(method, pattern)
	if !ok {
		return api.Errorf(api.CodeForbidden, "route %s %s has no permission entry", method, pattern)
	}
	return decide(held, e)
}

// CheckPath authorises held for a concrete request path by matching it
// against the table, for layers that see paths instead of patterns.
func (a *Authorizer) CheckPath(held api.Permission, method, path string) error {
	e, ok := a.Reg.Match(method, path)
	if !ok {
		return api.Errorf(api.CodeForbidden, "route %s %s has no permission entry", method, path)
	}
	return decide(held, e)
}

func decide(held api.Permission, e Entry) error {
	if e.Public {
		return nil
	}
	if held == "" {
		return api.Errorf(api.CodeUnauthorized, "authentication required")
	}
	if held.Satisfies(e.Perm) {
		return nil
	}
	return api.Errorf(api.CodeForbidden, "%s role cannot %s %s", held, e.Method, e.Pattern).
		WithDetail("required", string(e.Perm))
}
