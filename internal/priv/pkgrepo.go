package priv

import (
	"context"
	"io"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	rePkgURL     = regexp.MustCompile(`^(pkg\+)?https?://[A-Za-z0-9._~:/?&=%+@${}-]{1,300}$`)
	rePkgKeyLine = regexp.MustCompile(`^\s*([a-z_]+)\s*:\s*(?:"([^"\n]*)"|([A-Za-z0-9_]+))\s*,?\s*$`)
)

// verifyPkg checks, when pkg is pointed at a repository directory with -o REPOS_DIR, that the repository
// configuration there is one JailOps writes: a signed repository with a plain http(s) URL. The directory is
// written by the daemon (through the helper), and pkg runs as root, so a repository without a signature check, or
// with an odd URL scheme, would let the daemon install what it likes into a jail root as root.
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
	if !rePkgURL.MatchString(seen["url"]) {
		return forbidden("pkg repository %s: url %q is not an http(s) URL", name, seen["url"])
	}
	if seen["signature_type"] != "fingerprints" {
		return forbidden("pkg repository %s: the repository must be verified by fingerprints", name)
	}
	fp := seen["fingerprints"]
	if strings.HasPrefix(fp, "/usr/share/keys/") && !strings.Contains(fp, "..") {
		// the keys the base system ships
	} else if err := l.cfg.checkRulePath(pathState, fp); err != nil {
		return forbidden("pkg repository %s: fingerprints %q: %v", name, seen["fingerprints"], err)
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
