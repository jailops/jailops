package priv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"jailops.net/jailops/sdk/api"
)

var (
	rePkgKeyLine = regexp.MustCompile(`^\s*([a-z_]+)\s*:\s*(?:"([^"\n]*)"|([A-Za-z0-9_]+))\s*,?\s*$`)
)

// verifyPkg checks what a pkg command line names that does not depend on the repository directory: where it may work
// (verifyPkgTarget). The repository directory is checked and sealed when the command runs (sealRepos).
func verifyPkg(ctx context.Context, l *Local, args []string) error {
	if err := verifyPkgTarget(ctx, l, args); err != nil {
		return err
	}
	roots, _ := pkgTarget(args)
	if len(roots) != 1 {
		return nil
	}
	if l != nil && l.jailRoot(filepath.Clean(roots[0])) {
		// the root of a jail, which is upgraded while it runs: pkg's own trees are checked on every pkg -r, and a link
		// there names the jail as suspected (the daemon reports it and leaves the jail). The paths of the base are
		// checked once, before the upgrade (CheckRoot).
		if err := verifyPkgTree(l, roots[0]); err != nil && api.IsCode(err, api.CodeForbidden) {
			return forbidden("%s%s", SuspectedPrefix, findingOf(err, filepath.Clean(roots[0])))
		} else if err != nil {
			return err
		}
		return nil
	}
	if err := verifyPkgTree(l, roots[0]); err != nil {
		return err
	}
	switch pkgSubcommand(args) {
	case "install", "upgrade", "delete", "remove", "autoremove":
		return verifyRootLinks(roots[0])
	}
	return nil
}

// SuspectedPrefix starts the refusal of a jail's root in which the checks found what only a tenant preparing an
// attack on host-side pkg puts there: the daemon reports the jail as suspected of being compromised (high severity),
// leaves it alone and goes on with the others.
const SuspectedPrefix = "suspected_compromised: "

// pkgSubcommand is the first argument after pkg's global flags.
func pkgSubcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-r" || a == "-j" || a == "-o" || a == "-C":
			i++
		case strings.HasPrefix(a, "-"):
		default:
			return a
		}
	}
	return ""
}

// maxRootEntries bounds the walk of verifyRootLinks.
const maxRootEntries = 5_000_000

// shippedRootLinks are the links out of the root that FreeBSD's base itself makes, path to target: the termcap the base
// package ships (the only absolute link of the minimal sets of 15.1 and 16.0, verified) and the motd the rc script
// writes at boot. They are the same in every jail.
var shippedRootLinks = map[string]string{"etc/termcap": "/usr/share/misc/termcap", "etc/motd": "/var/run/motd"}

// untouchedTrees are the trees of a root that no base package installs into (verified over the files and directories
// of the base packages of 16.0): the ports packages, the homes, the data a service keeps. A link there is the jail's
// own business, except one to a directory of the host.
var untouchedTrees = []string{"usr/local/", "home/", "usr/home/", "srv/"}

// verifyRootLinks refuses a root that has a symbolic link leading out of it (its target absolute, or relative and
// climbing above the root), whatever it points to, outside untouchedTrees: pkg, as root of the host, follows such a link
// both when it creates the directories of a package (verified on FreeBSD 15 with pkg 2.9.2: ROOT/etc/ssl -> /tmp/hostssl
// made /tmp/hostssl/certs on the host) and when it reads a configuration file to merge it (verified by the second audit:
// ROOT/etc/crontab -> a host file put the host file's content into the jail). The links the base ships
// (shippedRootLinks) are kept; inside untouchedTrees only a link to a directory of the host is refused. A link that
// stays in the root (etc/unbound -> ../var/unbound) is fine.
func verifyRootLinks(root string) error {
	root = filepath.Clean(root)
	n := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if n++; n > maxRootEntries {
			return forbidden("pkg -r %s: more than %d entries in the root", root, maxRootEntries)
		}
		if d.Type()&fs.ModeSymlink == 0 {
			return nil
		}
		t, err := os.Readlink(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		host := t
		if !filepath.IsAbs(t) {
			inside := filepath.Join(filepath.Dir(rel), t)
			if inside != ".." && !strings.HasPrefix(inside, "../") {
				return nil
			}
			host = filepath.Join(filepath.Dir(p), t)
		}
		if shippedRootLinks[rel] == t {
			return nil
		}
		untouched := false
		for _, u := range untouchedTrees {
			untouched = untouched || strings.HasPrefix(rel, u)
		}
		if !untouched {
			return forbidden("pkg -r %s: %s is a symbolic link out of the root (to %s); remove it, or make it point inside the jail", root, rel, t)
		}
		if fi, err := os.Stat(host); err == nil && fi.IsDir() {
			return forbidden("pkg -r %s: %s is a symbolic link out of the root to the directory %s", root, rel, t)
		}
		return nil
	})
	if err != nil {
		var ae *api.Error
		if errors.As(err, &ae) {
			return err
		}
		return forbidden("pkg -r %s: the root cannot be read: %v", root, err)
	}
	return nil
}

// pkgTreeLinks are the directories of a jail root that pkg, running as root of the host, reads and writes: its
// configuration, the repository files, the cache, the package database. A jail that made one a symbolic link would
// send those writes through the host (the database of the host, say). The tree must have none of them as a link.
var pkgTreeLinks = []string{"usr", "usr/local", "usr/local/etc", "usr/local/etc/pkg", "usr/local/etc/pkg/repos", "etc", "etc/pkg",
	"var", "var/cache", "var/db", "var/db/pkg", "var/db/pkg/repos", "var/db/pkg/local.sqlite"}

// pkgTreeWalk are the trees of a jail root that pkg, running as root of the host, opens by plain path below the fixed
// components of pkgTreeLinks: the package database and its repository catalogues (SQLite files, lock files), the
// package cache and the configuration directories. pkg resolves the files it extracts beneath the root, but these are
// opened as they are: a symbolic link anywhere in them (var/db/pkg/repos/jailops -> /some/host/dir) would send pkg's
// writes to the host. No entry of these trees may be a symbolic link, except one to a file of its own directory (the
// cache links pkg makes itself).
var pkgTreeWalk = []string{"var/db/pkg", "var/cache/pkg", "usr/local/etc/pkg", "etc/pkg"}

// maxPkgTreeEntries bounds the walk of pkgTreeWalk: a tree with more entries is refused, not walked for ever.
const maxPkgTreeEntries = 200_000

// verifyPkgTree refuses a root where one of pkgTreeLinks is a symbolic link, or where any entry of the trees of
// pkgTreeWalk is one. A path that does not exist is fine. The walk does not enter another file system (a volume). The
// root of a running jail can change after the check (root in the jail): this is a detection, not a bound (security-model.md).
func verifyPkgTree(l *Local, root string) error {
	root = filepath.Clean(root)
	for _, rel := range pkgTreeLinks {
		fi, err := os.Lstat(filepath.Join(root, rel))
		switch {
		case err != nil && os.IsNotExist(err):
		case err != nil:
			return invalid("pkg -r %s: %s cannot be read: %v", root, rel, err)
		case fi.Mode()&os.ModeSymlink != 0:
			return forbidden("pkg -r %s: %s is a symbolic link", root, rel)
		}
	}
	n := 0
	for _, top := range pkgTreeWalk {
		base := filepath.Join(root, top)
		if _, err := os.Lstat(base); err != nil && os.IsNotExist(err) {
			continue
		}
		dev := deviceOf(base)
		err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && p != base && deviceOf(p) != dev {
				return filepath.SkipDir // another file system mounted here (a volume): not the root's
			}
			if n++; n > maxPkgTreeEntries {
				return invalid("pkg -r %s: more than %d entries below %s", root, maxPkgTreeEntries, strings.Join(pkgTreeWalk, ", "))
			}
			if d.Type()&fs.ModeSymlink != 0 && !sameDirLink(p) {
				rel, _ := filepath.Rel(root, p)
				return forbidden("pkg -r %s: %s is a symbolic link", root, rel)
			}
			return nil
		})
		if err != nil {
			var ae *api.Error
			if errors.As(err, &ae) {
				return err
			}
			return invalid("pkg -r %s: %s cannot be read: %v", root, top, err)
		}
	}
	return nil
}

// deviceOf is the device of the file system p is on, 0 when it cannot be read.
func deviceOf(p string) uint64 {
	var st syscall.Stat_t
	if err := syscall.Lstat(p, &st); err != nil {
		return 0
	}
	return uint64(st.Dev) //nolint:unconvert // the type of Dev differs between systems
}

// sameDirLink says whether the symbolic link p names a file of its own directory ("bash-5.3.20.pkg" ->
// "bash-5.3.20~1a2b3c.pkg", the links pkg itself makes in its cache, verified with pkg 2.9.2): such a link cannot lead
// out of the tree, and the entry it names is walked and checked too.
func sameDirLink(p string) bool {
	t, err := os.Readlink(p)
	return err == nil && t != "" && t != "." && t != ".." && !strings.ContainsRune(t, '/')
}

// hardenPkgRoot is the argv of a `pkg -r ROOT` with what keeps the root's own files out of pkg's configuration: pkg
// with -r reads ROOT/usr/local/etc/pkg.conf, and a jail root is its tenant's, who could load a plugin into this pkg
// (code as root of the host) or move its database. -C names an empty configuration file the helper makes for this run
// (pkg reads -C on the host, not below the root), and plugins are off. For the root of a jail the package scripts do not
// run either, unless no jail has run in it yet and none runs in it now (scriptsAllowed) (RUN_SCRIPTS=false): they run the host's programs on paths of the root, and a symbolic link the
// tenant planted would make them write, overwrite or read files of the host. What those scripts do is done inside the
// started jail afterwards (the upgrade's settle step). A pkg command without -r (pkg -j, pkg config abi) is returned
// as it is. The returned function removes the configuration file.
func (l *Local) hardenPkgRoot(ctx context.Context, argv []string) ([]string, func(), error) {
	none := func() {}
	if len(argv) < 2 {
		return argv, none, nil
	}
	roots, _ := pkgTarget(argv[1:])
	if len(roots) != 1 {
		return argv, none, nil
	}
	dir, err := os.MkdirTemp(l.cfg.StateDir, ".pkgconf-")
	if err != nil {
		return nil, none, fmt.Errorf("the configuration of pkg: %w", err)
	}
	cleanup := func() { os.RemoveAll(dir) }
	conf := filepath.Join(dir, "pkg.conf")
	if err := os.WriteFile(conf, nil, 0o600); err != nil {
		cleanup()
		return nil, none, fmt.Errorf("the configuration of pkg: %w", err)
	}
	extra := []string{"-C", conf, "-o", "PKG_ENABLE_PLUGINS=false"}
	if l.jailRoot(roots[0]) && !l.scriptsAllowed(ctx, roots[0]) {
		extra = append(extra, "-o", "RUN_SCRIPTS=false")
	}
	out := append(append([]string{argv[0]}, extra...), argv[1:]...)
	return out, cleanup, nil
}

// jailRoot says whether a path is the root of a jail by its place: <storage>/projects/P/jails/J/root.
func (l *Local) jailRoot(root string) bool {
	r := l.cfg.mountRel(filepath.Clean(root))
	s := strings.Split(r.rel, "/")
	return r.root != "" && len(s) == 5 && s[0] == "projects" && s[2] == "jails" && s[4] == "root"
}

// scriptsAllowed says whether package scripts may run on a jail root: no jail has run in it yet (NeverStartedFile)
// and no jail runs in it now. A list of the jails that cannot be read says no.
func (l *Local) scriptsAllowed(ctx context.Context, root string) bool {
	if !l.neverStarted(root) {
		return false
	}
	if l.d.Jails == nil {
		return true
	}
	list, err := l.d.Jails.List(ctx)
	if err != nil {
		return false
	}
	for _, j := range list {
		if jp, r := filepath.Clean(j.Path), filepath.Clean(root); j.Path != "" && (within(jp, r) || within(r, jp)) {
			return false
		}
	}
	return true
}

// NeverStartedFile is the file in the container of a jail that says no jail has run in its root yet: the daemon writes it
// when it makes the jail and removes it at the first start. Its root is then the copy of the image and nothing a tenant
// could have prepared, so the package scripts of an installation from the host may run.
const NeverStartedFile = "never-started"

// neverStarted says whether the container of the jail root holds NeverStartedFile (a regular file, not a link).
func (l *Local) neverStarted(root string) bool {
	r := l.cfg.mountRel(filepath.Clean(root))
	s := strings.Split(r.rel, "/")
	if r.root == "" || len(s) != 5 {
		return false
	}
	fi, err := os.Lstat(filepath.Join(r.root, s[0], s[1], s[2], s[3], NeverStartedFile))
	return err == nil && fi.Mode().IsRegular()
}

const (
	maxRepoEntries = 32       // entries a repository directory may hold: more is not one JailOps writes
	maxRepoFile    = 16 << 10 // bytes of a repository configuration: a longer file is refused, never cut short
)

var reRepoBlock = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}\s*:\s*\{$`)

// sealRepos is what makes the repository check mean something. pkg runs the scripts of a package as root on the host,
// so a repository, or a key, that the daemon chose would let it run what it likes as root; and the daemon writes the
// directory pkg reads its repositories from, and can change it between the check and the run. The helper therefore
// reads the whole directory itself (every entry, every byte), checks every configuration, copies exactly the bytes it
// checked into a directory it creates for this run (root's, mode 0700) and gives pkg that one. The returned function
// removes it.
func (l *Local) sealRepos(ctx context.Context, argv []string) ([]string, func(), error) {
	none := func() {}
	dir := reposDirOf(argv[1:])
	if dir == "" {
		return argv, none, nil
	}
	files, err := l.readRepoDir(dir)
	if err != nil {
		return nil, none, err
	}
	for _, f := range files {
		if err := l.checkRepoConf(f.name, string(f.body)); err != nil {
			return nil, none, err
		}
	}
	sealed, err := os.MkdirTemp(l.cfg.StateDir, ".pkgrepos-")
	if err != nil {
		return nil, none, fmt.Errorf("sealing the repository directory: %w", err)
	}
	cleanup := func() { os.RemoveAll(sealed) }
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(sealed, f.name), f.body, 0o600); err != nil {
			cleanup()
			return nil, none, fmt.Errorf("sealing the repository directory: %w", err)
		}
	}
	out := append([]string(nil), argv...)
	for i := 1; i+1 < len(out); i++ {
		if out[i] == "-o" && strings.HasPrefix(out[i+1], "REPOS_DIR=") {
			out[i+1] = "REPOS_DIR=" + sealed
		}
	}
	return out, cleanup, nil
}

type repoFile struct {
	name string
	body []byte
}

// readRepoDir reads the *.conf files of the directory pkg would read, all of them, each whole. It refuses a directory
// with more than maxRepoEntries entries, a configuration that is not a plain file or is longer than maxRepoFile.
func (l *Local) readRepoDir(dir string) ([]repoFile, error) {
	r, err := l.cfg.resolve(dir)
	if err != nil {
		return nil, err
	}
	root, err := r.openRoot()
	if err != nil {
		return nil, mapFSErr(err)
	}
	defer root.Close()
	d, err := root.Open(r.rel)
	if err != nil {
		return nil, mapFSErr(err)
	}
	names, err := d.Readdirnames(maxRepoEntries + 1)
	d.Close()
	if err != nil && err != io.EOF {
		return nil, mapFSErr(err)
	}
	if len(names) > maxRepoEntries {
		return nil, forbidden("pkg repository directory: more than %d entries", maxRepoEntries)
	}
	var files []repoFile
	for _, n := range names {
		if !strings.HasSuffix(n, ".conf") {
			continue
		}
		f, err := root.OpenFile(filepath.Join(r.rel, n), os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			return nil, mapFSErr(err)
		}
		fi, err := f.Stat()
		if err != nil || !fi.Mode().IsRegular() {
			f.Close()
			return nil, forbidden("pkg repository %s is not a plain file", n)
		}
		b, err := io.ReadAll(io.LimitReader(f, maxRepoFile+1))
		f.Close()
		if err != nil {
			return nil, mapFSErr(err)
		}
		if len(b) > maxRepoFile {
			return nil, forbidden("pkg repository %s is longer than %d bytes", n, maxRepoFile)
		}
		files = append(files, repoFile{n, b})
	}
	return files, nil
}

// checkRepoConf accepts exactly the file JailOps writes (imagegen/source.go): one block, `name: {`, the keys below it
// one to a line, `}`. A key that appears twice is refused (pkg takes the first value, a check that read the last
// one would have approved something else), and so is every line that is not one of these.
func (l *Local) checkRepoConf(name, body string) error {
	seen := map[string]string{}
	state := 0 // 0 before the block, 1 inside, 2 after
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		switch {
		case state == 0 && reRepoBlock.MatchString(line):
			state = 1
			continue
		case state == 1 && line == "}":
			state = 2
			continue
		case state != 1:
			return forbidden("pkg repository %s: line %q is not one JailOps writes", name, line)
		}
		m := rePkgKeyLine.FindStringSubmatch(line)
		if m == nil {
			return forbidden("pkg repository %s: line %q is not one JailOps writes", name, line)
		}
		v := m[2]
		if v == "" {
			v = m[3]
		}
		if _, dup := seen[m[1]]; dup {
			return forbidden("pkg repository %s: key %q appears twice", name, m[1])
		}
		seen[m[1]] = v
	}
	if state != 2 {
		return forbidden("pkg repository %s: it is not one block of keys", name)
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

// verifyPkgTarget says where pkg may work. It runs as root of the host against the root it was given: the scripts of a
// package run there as host processes on paths of the root (PKG_ROOTDIR), and the database and the directories it
// writes are opened by their paths: a process of a running jail that replaced them (or put a symbolic link where pkg
// writes) would have root of the host. So `pkg -r ROOT` is for a root no jail is running
// in (an image being built, a jail that is stopped), and `pkg -j JAIL` is for a running jail: pkg enters the jail first,
// so what it does is confined to it and uses the jail's own repository configuration.
func verifyPkgTarget(ctx context.Context, l *Local, args []string) error {
	roots, jails := pkgTarget(args)
	if len(roots)+len(jails) == 0 && len(args) == 2 && args[0] == "config" && args[1] == "abi" {
		return nil // the ABI of the host, which names no root
	}
	if len(roots)+len(jails) != 1 {
		return invalid("pkg needs exactly one of -r ROOT (an image being built, or the root of a jail of JailOps) and -j JAIL (a running jail)")
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
		if !within(jp, real) && !within(jp, root) {
			continue
		}
		// the root of a managed jail is upgraded while the jail runs (the owner's decision: JailOps never stops a
		// jail for an upgrade); the administrator trusts a jail whose base he upgrades from the host (security-model.md)
		if _, managed := l.confFileFor(j.Name); managed && jp == root && l.jailRoot(root) {
			continue
		}
		return forbidden("pkg -r %s: it is the root of the running jail %s; stop the jail, or run pkg inside it with -j", roots[0], j.Name)
	}
	return nil
}
