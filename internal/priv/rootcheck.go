package priv

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"

	"jailops.net/jailops/sdk/api"
)

// RootCheckReq asks jail.check_root to look at the root of a jail before the host's pkg works on it: Paths are the
// files the installed base packages own (pkg query %Fp on a copy of the jail's package database), absolute in the jail.
type RootCheckReq struct {
	Root  string   `json:"root"`
	Paths []string `json:"paths"`
}

// maxRootCheckPaths bounds the paths of one check: the base of a jail has some thousands of files, never this many.
const maxRootCheckPaths = 200_000

// CheckRoot implements Host: the detection of a jail root prepared against host-side pkg, made once before an upgrade
// of the jail's base. pkg, as root of the host, follows a symbolic link that is a directory on the way to a file it
// installs (ROOT/etc/ssl -> /tmp/hostssl made /tmp/hostssl/certs on the host, verified with pkg 2.9.2), and reads a
// configuration file it merges through a link (ROOT/etc/crontab -> a host file put the host file into the jail). So
// what counts is the paths pkg touches: each file of the base packages and every directory above it, and pkg's own trees
// (verifyPkgTree). A link there that leads out of the root (absolute, or climbing above it) is a finding, except the
// links the base ships. Links anywhere else are the jail's own business: /etc/localtime -> /usr/share/zoneinfo/... as
// tzsetup makes it, /var/www -> /usr/local/www, the links in a volume. Nothing is walked but pkg's trees, and those
// stop at a mount point. A pkg configuration of the root that turns plugins on is a finding too, though pkg never reads
// it (-C). The answer is nil, or forbidden with SuspectedPrefix and every finding.
func (l *Local) CheckRoot(_ context.Context, q RootCheckReq) error {
	root := filepath.Clean(q.Root)
	if !l.jailRoot(root) {
		return forbidden("%s is not the root of a jail", q.Root)
	}
	if len(q.Paths) > maxRootCheckPaths {
		return invalid("more than %d paths to check", maxRootCheckPaths)
	}
	found, err := rootFindings(l, root, q.Paths)
	if err != nil || len(found) == 0 {
		return err
	}
	return forbidden("%s%s", SuspectedPrefix, strings.Join(found, "; "))
}

// rootFindings is the detection of CheckRoot, one finding per entry; the audit of a jail (AuditRoot) reports the same.
func rootFindings(l *Local, root string, paths []string) ([]string, error) {
	var found []string
	if err := verifyPkgTree(l, root); err != nil {
		if !api.IsCode(err, api.CodeForbidden) {
			return nil, err
		}
		found = append(found, findingOf(err, root))
	}
	// every component is resolved as the kernel does it, beneath the root (os.Root): a chain of links that stays in
	// the root lexically but leaves it once resolved (usr/share/certs -> ../x, usr/x -> /tmp/hostcerts) is caught
	jr, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer jr.Close()
	seen := map[string]bool{}
	for _, p := range append(slices.Clone(paths), pkgTreeRoots...) {
		rel := strings.TrimPrefix(filepath.Clean("/"+p), "/")
		if rel == "" || seen[rel] {
			continue
		}
		// the path itself and every directory above it, the shortest first
		parts := strings.Split(rel, "/")
		for i := 1; i <= len(parts); i++ {
			sub := strings.Join(parts[:i], "/")
			if seen[sub] {
				continue
			}
			seen[sub] = true
			if f := escapes(jr, sub); f != "" {
				found = append(found, f)
				break
			}
			fi, err := jr.Lstat(sub)
			if err != nil {
				break // not there: pkg makes it, and nothing below it exists either
			}
			if fi.Mode()&os.ModeSymlink == 0 {
				continue
			}
			t, err := jr.Readlink(sub)
			if err != nil || shippedRootLinks[sub] == t {
				break // a link the base ships: pkg replaces it, it goes nowhere else
			}
			if f := escapes(jr, sub, t); f != "" {
				found = append(found, f)
				break
			}
		}
	}
	for _, conf := range []string{"usr/local/etc/pkg.conf", "etc/pkg.conf"} {
		if pkgConfLoadsPlugins(filepath.Join(root, conf)) {
			found = append(found, conf+" turns pkg plugins on")
		}
	}
	return found, nil
}

// pkgTreeRoots are pkg's own trees, whose components are resolved like the paths of the base packages.
var pkgTreeRoots = []string{"var/db/pkg", "var/cache/pkg", "usr/local/etc/pkg", "etc/pkg"}

// escapes is the finding for a path of the root that does not resolve beneath it, "" when it does or does not exist.
// With the target t of a link at sub, the link itself is judged: it is followed (Stat) and must stay in the root.
func escapes(jr *os.Root, sub string, t ...string) string {
	var err error
	if len(t) == 0 {
		_, err = jr.Lstat(sub)
	} else {
		if leavesRoot(sub, t[0]) {
			return sub + " is a symbolic link out of the root (to " + t[0] + "); remove it, or make it point inside the jail"
		}
		_, err = jr.Stat(sub)
	}
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	if len(t) > 0 {
		return sub + " is a symbolic link (to " + t[0] + ") whose path escapes the root through another link; remove it, or make it point inside the jail"
	}
	return sub + ": the path escapes the root through a symbolic link above it; remove that link, or make it point inside the jail"
}

// leavesRoot says whether the link at rel with target t leads out of the root: an absolute target, or a relative one
// that climbs above it.
func leavesRoot(rel, t string) bool {
	if filepath.IsAbs(t) {
		return true
	}
	inside := filepath.Join(filepath.Dir(rel), t)
	return inside == ".." || strings.HasPrefix(inside, "../")
}

// findingOf is the text of a refusal of verifyPkgTree without its "pkg -r ROOT: " prefix.
func findingOf(err error, root string) string {
	return strings.TrimPrefix(strings.TrimPrefix(err.Error(), "forbidden: "), "pkg -r "+root+": ")
}

var rePkgPlugins = regexp.MustCompile(`(?im)^\s*(pkg_enable_plugins\s*[:=]\s*"?(true|yes|on)"?|plugins\s*[:=\[])`)

// pkgConfLoadsPlugins says whether the pkg configuration at p turns plugins on or names plugins: a setting, not a
// comment (the file pkg installs has them all commented out).
func pkgConfLoadsPlugins(p string) bool {
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return false
	}
	defer f.Close()
	b, _ := io.ReadAll(io.LimitReader(f, 1<<20))
	return rePkgPlugins.Match(b)
}
