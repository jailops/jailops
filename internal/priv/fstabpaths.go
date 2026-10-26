package priv

import (
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
)

// A jail's fstab is read by mount(8) on the host, and the host resolves symbolic links in it. The checks of
// checkFstab are on the text; these are on the file system: no component below the root a path belongs to may be a
// symbolic link, or a jail's root user could redirect a mount point or a source onto the host by replacing a
// directory of its own tree with a link. They run when a file is written and again when the jail is started, which
// is when the jail is stopped and nothing of it can change the tree except another jail that shares it.

// evalPrefix resolves symbolic links in the longest existing prefix of p and appends the rest unresolved.
func evalPrefix(p string) (string, error) {
	cur, rest := filepath.Clean(p), ""
	for {
		r, err := filepath.EvalSymlinks(cur)
		if err == nil {
			return filepath.Join(r, rest), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", err
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// plainBelow requires that p, which lies below root, reaches the same place with and without resolving symbolic
// links below root. Links above root (the root's own path) are the operator's business.
func plainBelow(root, p string) error {
	realRoot, err := evalPrefix(root)
	if err != nil {
		return forbidden("cannot resolve %q: %v", root, err)
	}
	real, err := evalPrefix(p)
	if err != nil {
		return forbidden("cannot resolve %q: %v", p, err)
	}
	rel, err := filepath.Rel(root, p)
	if err != nil || strings.HasPrefix(rel, "..") {
		return forbidden("%q is outside %q", p, root)
	}
	if real != filepath.Join(realRoot, rel) {
		return forbidden("%q resolves through a symbolic link to %q; mount points and sources may not contain links", p, real)
	}
	return nil
}

// checkFstabPaths applies the file system checks to every mount point and every nullfs source of an fstab. jailRoot
// is the jail's path (its root dataset's mountpoint), "" when the file is checked on its own, in which case a mount
// point is held to the storage mount it lies in.
func (c Config) checkFstabPaths(text, jailRoot string) error {
	for i, e := range parseFstab(text) {
		base := ""
		if jailRoot != "" {
			if !within(jailRoot, e.mp) {
				return forbidden("fstab line %d: mount point %q is outside the jail's root %s", i+1, e.mp, jailRoot)
			}
			base = jailRoot
		} else {
			for _, m := range c.reg.mounts() {
				if within(m, e.mp) {
					base = m
				}
			}
		}
		if base != "" {
			if err := plainBelow(base, e.mp); err != nil {
				return forbidden("fstab line %d: %v", i+1, err)
			}
		}
		if e.typ != "nullfs" {
			continue
		}
		src := ""
		for _, m := range c.reg.mounts() {
			if within(m, e.dev) {
				src = m
			}
		}
		if src == "" {
			for _, r := range c.Admin.HostMountRoots {
				if within(r, e.dev) {
					src = r
				}
			}
		}
		if src == "" {
			return forbidden("fstab line %d: source %q is outside every approved directory", i+1, e.dev)
		}
		if err := plainBelow(src, e.dev); err != nil {
			return forbidden("fstab line %d: %v", i+1, err)
		}
	}
	return nil
}

type fstabEntry struct {
	dev, mp, typ, opts string
	line               string // the fields joined by single spaces
}

// parseFstab returns the well-formed lines; malformed ones are checkFstab's to refuse.
func parseFstab(text string) []fstabEntry {
	var out []fstabEntry
	for _, l := range strings.Split(text, "\n") {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		f := strings.Fields(t)
		if len(f) != 6 {
			continue
		}
		out = append(out, fstabEntry{dev: f[0], mp: f[1], typ: f[2], opts: f[3], line: strings.Join(f, " ")})
	}
	return out
}

// confParam returns the unquoted value of a parameter of a jail.conf that checkJailConf accepted.
func confParam(text, key string) (string, bool) {
	for _, l := range strings.Split(text, "\n") {
		m := paramRE.FindStringSubmatch(strings.TrimLeft(l, "\t"))
		if m == nil || m[1] != key {
			continue
		}
		if v, ok := unquote(m[2]); ok {
			return v, true
		}
	}
	return "", false
}
