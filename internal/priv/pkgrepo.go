package priv

import (
	"context"
	"io"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	rePkgKeyLine = regexp.MustCompile(`^\s*([a-z_]+)\s*:\s*(?:"([^"\n]*)"|([A-Za-z0-9_]+))\s*,?\s*$`)
)

// verifyPkg checks, when pkg is pointed at a repository directory with -o REPOS_DIR, that the repository
// configuration there is one JailOps writes: a repository on a host root listed (fetch-host), checked against a key
// directory that root provides (the base system's, or pkg-keys-dir). The directory is written by the daemon
// (through the helper) and pkg runs install scripts as root, so a repository, or a key, that the daemon chose would
// let it run what it likes as root.
func verifyPkg(_ context.Context, l *Local, args []string) error {
	for i, a := range args {
		if a != "-o" || i+1 >= len(args) {
			continue
		}
		dir, ok := strings.CutPrefix(args[i+1], "REPOS_DIR=")
		if !ok {
			continue
		}
		r, err := l.cfg.resolve(dir)
		if err != nil {
			return err
		}
		root, err := r.openRoot()
		if err != nil {
			return mapFSErr(err)
		}
		defer root.Close()
		d, err := root.Open(r.rel)
		if err != nil {
			return mapFSErr(err)
		}
		names, err := d.Readdirnames(64)
		d.Close()
		if err != nil && err != io.EOF {
			return mapFSErr(err)
		}
		for _, n := range names {
			if !strings.HasSuffix(n, ".conf") {
				continue
			}
			f, err := root.Open(filepath.Join(r.rel, n))
			if err != nil {
				return mapFSErr(err)
			}
			b, err := io.ReadAll(io.LimitReader(f, 8<<10))
			f.Close()
			if err != nil {
				return mapFSErr(err)
			}
			if err := l.checkRepoConf(n, string(b)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (l *Local) checkRepoConf(name, body string) error {
	seen := map[string]string{}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasSuffix(line, "{") || line == "}" {
			continue
		}
		m := rePkgKeyLine.FindStringSubmatch(line)
		if m == nil {
			return forbidden("pkg repository %s: line %q is not one JailOps writes", name, line)
		}
		v := m[2]
		if v == "" {
			v = m[3]
		}
		seen[m[1]] = v
	}
	if err := l.cfg.checkPkgURL(seen["url"]); err != nil {
		return forbidden("pkg repository %s: %v", name, err)
	}
	if seen["signature_type"] != "fingerprints" {
		return forbidden("pkg repository %s: the repository must be verified by fingerprints", name)
	}
	if err := l.cfg.checkPkgKeys(seen["fingerprints"]); err != nil {
		return forbidden("pkg repository %s: %v", name, err)
	}
	for k := range seen {
		switch k {
		case "url", "signature_type", "fingerprints", "enabled", "mirror_type", "priority", "ip_version":
		default:
			return forbidden("pkg repository %s: key %q is not accepted", name, k)
		}
	}
	return nil
}
