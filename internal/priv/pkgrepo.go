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
func verifyPkg(ctx context.Context, l *Local, args []string) error {
	if err := verifyPkgTarget(ctx, l, args); err != nil {
		return err
	}
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

// pkgTarget returns the -r ROOT and -j JAIL among the flags that come before the subcommand.
func pkgTarget(args []string) (roots, jails []string) {
	for i := 0; i < len(args) && strings.HasPrefix(args[i], "-"); i++ {
		switch args[i] {
		case "-r", "-j":
			if i+1 >= len(args) {
				return
			}
			if args[i] == "-r" {
				roots = append(roots, args[i+1])
			} else {
				jails = append(jails, args[i+1])
			}
			i++
		case "-o":
			i++
		}
	}
	return
}

// verifyPkgTarget says where pkg may work. It runs as root, and it runs the scripts of a package in a chroot of the
// root it was given, with the shell and the libraries found there: a process of a running jail that replaced them (or
// put a symbolic link where pkg writes) would have root of the host. So `pkg -r ROOT` is for a root no jail is running
// in (an image being built, a jail that is stopped), and `pkg -j JAIL` is for a running jail: pkg enters the jail first,
// so what it does is confined to it and uses the jail's own repository configuration.
func verifyPkgTarget(ctx context.Context, l *Local, args []string) error {
	roots, jails := pkgTarget(args)
	if len(roots)+len(jails) == 0 && len(args) == 2 && args[0] == "config" && args[1] == "abi" {
		return nil // the ABI of the host, which names no root
	}
	if len(roots)+len(jails) != 1 {
		return invalid("pkg needs exactly one of -r ROOT (a root no jail runs in) and -j JAIL (a running jail)")
	}
	if len(jails) == 1 {
		name := jails[0]
		if err := ValidJailName(name); err != nil {
			return err
		}
		if _, ok := l.confFileFor(name); !ok {
			return forbidden("jail %q is not managed by JailOps", name)
		}
		for i, a := range args {
			if a == "-o" && i+1 < len(args) {
				if k, _, _ := strings.Cut(args[i+1], "="); k == "REPOS_DIR" || k == "PKG_CACHEDIR" {
					return forbidden("pkg -o %s is for a root: with -j pkg reads the jail's own configuration", k)
				}
			}
		}
		j, ok, err := l.d.Jails.Get(ctx, name)
		if err != nil {
			return err
		}
		if !ok {
			return invalid("jail %q is not running: pkg -j runs inside it", name)
		}
		_ = j
		return nil
	}
	root := filepath.Clean(roots[0])
	real := root
	if r, err := filepath.EvalSymlinks(root); err == nil {
		real = r
	}
	list, err := l.d.Jails.List(ctx)
	if err != nil {
		return err
	}
	for _, j := range list {
		if j.Path == "" {
			continue
		}
		jp := filepath.Clean(j.Path)
		if r, err := filepath.EvalSymlinks(jp); err == nil {
			jp = r
		}
		if within(jp, real) || within(jp, root) {
			return forbidden("pkg -r %s: it is the root of the running jail %s; stop the jail, or run pkg inside it with -j", roots[0], j.Name)
		}
	}
	return nil
}
