package priv

import (
	"path"
	"strings"

	"jailops.net/jailops/internal/render"
	"jailops.net/jailops/internal/validate"
	"jailops.net/jailops/sdk/api"
)

// MaxInternalName is the longest jail key (the OS-level jail name).
const MaxInternalName = validate.MaxJailKeyLen

// A jail is known to the host by its project-qualified key, <project>_<name>;
// a key without a project does not exist. The key is
// the name of the jail.conf block, of <key>.conf and <key>.fstab in the conf
// directory, of the rendered PF anchor file (rendered/pf/<key>.conf), of the
// jail in jail(8) and the argument of the hook.
//
// The dataset of a jail is <storage root>/projects/<project>/jails/<name> and
// its PF anchor jailops/<project>/<name>.

// JailName is a parsed key.
type JailName struct {
	Project string
	Name    string
}

// Internal returns the key.
func (n JailName) Internal() string { return n.Project + "_" + n.Name }

// ParseJailName validates a jail key (exactly the keys the service layer
// produces) and splits it.
func ParseJailName(key string) (JailName, error) {
	if err := validate.JailKey("name", key); err != nil || strings.ContainsAny(key, "/\x00\n ") {
		return JailName{}, api.Errorf(api.CodeInvalid, "invalid jail name %q", key)
	}
	p, n, ok := strings.Cut(key, "_")
	if !ok || p == "" || n == "" || strings.Contains(n, "_") ||
		validate.Name("project", p) != nil || validate.Name("name", n) != nil {
		return JailName{}, api.Errorf(api.CodeInvalid, "invalid jail name %q: a jail key is <project>_<name>", key)
	}
	return JailName{Project: p, Name: n}, nil
}

// ValidJailName reports whether key is an acceptable jail key.
func ValidJailName(key string) error { _, err := ParseJailName(key); return err }

// JailAnchor returns the PF anchor of a jail: jailops/<project>/<name>.
func JailAnchor(n JailName) string { return render.JailAnchorFor(n.Project, n.Name) }

// jailDatasets returns the candidate container datasets of a jail inside a
// storage root, preferred first.
func jailDatasets(root string, n JailName) []string {
	return []string{path.Join(root, "projects", n.Project, "jails", n.Name)}
}
