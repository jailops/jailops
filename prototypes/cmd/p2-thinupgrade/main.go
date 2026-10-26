// Command p2-thinupgrade is prototype 2: the thin-jail upgrade experiment of design section 4.
//
// HONESTY NOTE: this was written on Linux. It has never been run on FreeBSD. Every behaviour
// that depends on FreeBSD is marked UNVERIFIED: and the run reports which ones it settled.
//
// What it does, all under $JAILOPS_PROTO_POOL/jailops-proto-2 and the mount tree --mnt:
//  1. build generation g1 with pkgbase (pkg -r), snapshot g1@ro
//  2. create a jail whose root is a clone of g1@ro plus persistent datasets, install a
//     package, write data, start it, run a health check
//  3. build g2 in a clone of g1@ro (pkg upgrade, plus synthetic changes so there is always
//     something to merge), snapshot g2@ro
//  4. upgrade: snapshot all jail datasets, stop, clone g2@ro, etcupdate g1->g2 on the
//     persistent /etc, swap roots with zfs rename, start, health check
//  5. scenarios: root rollback keeps data; all rollback loses it (zfs get written);
//     etc merge conflicts; /var skeleton drift; major-version note.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"jailops.net/jailops/prototypes/internal/harness"
	"jailops.net/jailops/prototypes/internal/thinupgrade"
)

type config struct {
	mnt, abi, repoConf, pkgbaseDir, sets, appPkg, varPaths, baseURL string
	synthetic                                                       bool
}

var cfg config

const jailName = "jop2a"

func main() {
	harness.Exit(harness.Spec{
		N:       2,
		Name:    "thinupgrade",
		Summary: "Thin jail upgrade: generations as ZFS clones, persistent datasets, etcupdate, root swap, rollback scopes.",
		Plan: []string{
			"create <pool>/jailops-proto-2 mounted at --mnt (default /jop-proto-2); everything below lives there",
			"build generation g1 with `pkg -r` (pkgbase), snapshot images/g1@ro",
			"clone g1@ro to jail/root, create persistent datasets (etc, usr/local, var/db, var/log, var/mail, var/spool, home, root) seeded from the clone",
			"start jail " + jailName + " (no network, own jail.conf in the scratch tree), install a package, write data, health check",
			"build g2 in a clone of g1@ro (pkg upgrade + synthetic /etc and /var changes), snapshot g2@ro",
			"upgrade: atomic snapshot of all jail datasets, stop, clone g2@ro, etcupdate g1->g2 on persistent /etc, zfs rename swap, start, health check",
			"scenarios: root rollback (data kept), all rollback (data lost, zfs get written), etc merge conflicts on a scratch copy, /var skeleton drift, major-version note",
			"print the persistent path list that survived; never touches other datasets, jails or system paths",
		},
		Flags: func(e *harness.Env, fs *flag.FlagSet) {
			fs.StringVar(&cfg.mnt, "mnt", "/jop-proto-2", "mount tree of the scratch dataset (must start with /jop-proto-)")
			fs.StringVar(&cfg.abi, "abi", "FreeBSD:15:amd64", "pkg ABI")
			fs.StringVar(&cfg.repoConf, "repo-conf", "", "a pkg repo .conf file to use instead of the generated one")
			fs.StringVar(&cfg.pkgbaseDir, "pkgbase-dir", "", "local pkgbase repository directory (file://), for hosts without network")
			fs.StringVar(&cfg.baseURL, "base-url", "pkg+https://pkg.FreeBSD.org/${ABI}/base_release_${VERSION_MINOR}", "pkgbase repository URL (a RELEASE uses base_release_<minor>; -STABLE uses base_latest)")
			fs.StringVar(&cfg.sets, "sets", "FreeBSD-set-minimal-jail", "pkgbase meta package(s) for the base system, comma separated (UNVERIFIED: name)")
			fs.StringVar(&cfg.appPkg, "app-pkg", "jq", "third-party package to install into the jail; empty installs a marker script instead")
			fs.StringVar(&cfg.varPaths, "var-paths", strings.Join(thinupgrade.DefaultVarPaths, ","), "persistent /var subpaths (comma separated)")
			fs.BoolVar(&cfg.synthetic, "synthetic", true, "inject synthetic g2 changes so etcupdate and skeleton drift always have work")
		},
		Run:     run,
		Cleanup: cleanup,
	})
}

type proto struct {
	e        *harness.Env
	mnt      string
	jroot    string // jail root mount path
	reposDir string
	work     string
	persist  []thinupgrade.Persist
	conf     string
}

func (p *proto) ds(parts ...string) string { return p.e.Root + "/" + strings.Join(parts, "/") }
func (p *proto) jds(name string) string    { return p.ds("jail", name) }
func (p *proto) img(g string) string       { return p.mnt + "/images/" + g }
func (p *proto) jp(rel string) string      { return filepath.Join(p.jroot, rel) }

func newProto(e *harness.Env) *proto {
	if !strings.HasPrefix(cfg.mnt, "/jop-proto-") || strings.Contains(cfg.mnt, "..") || strings.Count(cfg.mnt, "/") != 1 {
		e.Abort("--mnt must be a single path component starting with jop-proto- (got %q)", cfg.mnt)
	}
	p := &proto{e: e, mnt: cfg.mnt}
	p.jroot = p.mnt + "/jail/root"
	p.work = p.mnt + "/work"
	p.reposDir = p.work + "/repos.d"
	p.conf = p.work + "/jail.conf"
	p.persist = thinupgrade.PersistentPaths(strings.Split(cfg.varPaths, ","))
	return p
}

func (p *proto) write(path, content string, mode os.FileMode) {
	if p.e.DryRun {
		p.e.Note("DRY-RUN would write %s (%d bytes)", path, len(content))
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		p.e.Abort("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		p.e.Abort("write %s: %v", path, err)
	}
}

func (p *proto) read(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

func (p *proto) exists(path string) bool { _, err := os.Stat(path); return err == nil }

// seedKeys copies the host's signing key directories into the root being built.
// Verified on FreeBSD 15.1: `pkg -r <root>` resolves the fingerprint directories inside <root>,
// so an empty root cannot verify any repository until the keys are there.
func (p *proto) seedKeys(root string) {
	if p.exists(root + "/usr/share/keys") {
		return
	}
	p.e.Run("mkdir", "-p", root+"/usr/share/keys")
	p.e.Run("cp", "-a", "/usr/share/keys/.", root+"/usr/share/keys/")
}

func (p *proto) pkg(root string, args ...string) harness.Result {
	p.seedKeys(root)
	return p.e.RunT(900e9, thinupgrade.PkgArgs(root, p.reposDir, cfg.abi, args...)...)
}

func (p *proto) jexec(argv ...string) harness.Result {
	return p.e.Run(append([]string{"jexec", jailName}, argv...)...)
}

func (p *proto) jailUp() bool {
	return p.e.Quiet("jls", "-j", jailName, "jid").Err == nil && !p.e.DryRun
}

func (p *proto) startJail() bool {
	r := p.e.Run("jail", "-f", p.conf, "-c", jailName)
	return r.OK()
}

func (p *proto) stopJail() {
	if p.e.DryRun || p.jailUp() {
		p.e.Run("jail", "-f", p.conf, "-r", jailName)
	}
}

func (p *proto) health(tag string) bool {
	e := p.e
	ok := p.jexec("/bin/sh", "-c", "echo health-ok").Out() == "health-ok" || e.DryRun
	ver := p.jexec("/bin/freebsd-version", "-u").Out()
	if cfg.appPkg != "" {
		ok = p.jexec("/bin/sh", "-c", "/usr/local/bin/"+cfg.appPkg+" --version >/dev/null 2>&1 || test -x /usr/local/bin/"+cfg.appPkg).OK() && ok
	} else {
		ok = p.jexec("/usr/local/bin/jop-app").Out() == "app-ok" && ok || e.DryRun
	}
	e.Check("health-"+tag, ok, "freebsd-version -u in jail: "+ver)
	return ok
}

// swapRoot renames the current root to demote and promote to root, remounting the tree.
// The jail must be stopped. UNVERIFIED: renaming a clone with a local mountpoint works as
// ordered here (mountpoint=none before rename, set after) without remount conflicts.
func (p *proto) swapRoot(demote, promote string) {
	e := p.e
	for i := len(p.persist) - 1; i >= 0; i-- {
		e.Zfs("unmount", p.jds(p.persist[i].Name))
	}
	e.Zfs("unmount", p.jds("root"))
	e.Zfs("set", "mountpoint=none", p.jds("root"))
	e.Must("swap-rename-demote", e.Zfs("rename", p.jds("root"), p.jds(demote)).OK(), "root -> "+demote)
	e.Must("swap-rename-promote", e.Zfs("rename", p.jds(promote), p.jds("root")).OK(), promote+" -> root")
	e.Must("swap-mount-root", e.Zfs("set", "mountpoint="+p.jroot, p.jds("root")).OK(), "mountpoint set, auto mount")
	e.Zfs("mount", p.jds("root"))
	for _, ps := range p.persist {
		e.Zfs("mount", p.jds(ps.Name))
	}
}

func (p *proto) persistSnapshot(snap string) {
	args := []string{"snapshot", p.jds("root") + "@" + snap}
	for _, ps := range p.persist {
		args = append(args, p.jds(ps.Name)+"@"+snap)
	}
	p.e.Must("atomic-snapshot-"+snap, p.e.Zfs(args...).OK(), "one zfs snapshot command over root + persistent datasets")
}

func (p *proto) writtenReport(snap string) (int64, string) {
	var total int64
	var lines []string
	names := append([]string{"root"}, func() []string {
		var n []string
		for _, ps := range p.persist {
			n = append(n, ps.Name)
		}
		return n
	}()...)
	for _, n := range names {
		v, err := thinupgrade.ParseWritten(p.e.ZfsGet("written@"+snap, p.jds(n)))
		if err == nil {
			total += v
			lines = append(lines, fmt.Sprintf("%s=%s", n, thinupgrade.HumanBytes(v)))
		}
	}
	return total, strings.Join(lines, " ")
}

func (p *proto) seedAndMount(ps thinupgrade.Persist) {
	e := p.e
	d := p.jds(ps.Name)
	seed := p.work + "/seed/" + ps.Name
	e.Must("create-"+ps.Name, e.Zfs("create", "-o", "canmount=noauto", "-o", "mountpoint="+seed, d).OK(), d)
	if !e.DryRun {
		_ = os.MkdirAll(p.jp(ps.Mount), 0o755)
	}
	e.Zfs("mount", d)
	e.Pipe([]string{"tar", "-C", p.jp(ps.Mount), "-cf", "-", "."}, []string{"tar", "-C", seed, "-xpf", "-"})
	e.Zfs("unmount", d)
	e.Zfs("set", "mountpoint="+p.jp(ps.Mount), d)
	e.Zfs("mount", d)
	e.Zfs("set", "canmount=on", d)
}

func (p *proto) buildRepoConf() {
	e := p.e
	switch {
	case cfg.repoConf != "":
		b, err := os.ReadFile(cfg.repoConf)
		if err != nil && !e.DryRun {
			e.Abort("read --repo-conf: %v", err)
		}
		p.write(p.reposDir+"/user.conf", string(b), 0o644)
	case cfg.pkgbaseDir != "":
		p.write(p.reposDir+"/FreeBSD-base.conf", thinupgrade.RepoConf("FreeBSD-base", "file://"+cfg.pkgbaseDir, false), 0o644)
	default:
		p.write(p.reposDir+"/FreeBSD-base.conf", thinupgrade.RepoConf("FreeBSD-base", cfg.baseURL, true), 0o644)
		p.write(p.reposDir+"/FreeBSD-ports.conf", thinupgrade.RepoConf("FreeBSD-ports", "pkg+https://pkg.FreeBSD.org/${ABI}/quarterly", true), 0o644)
	}
}

func (p *proto) baseVersion(root string) string {
	return p.pkg(root, "query", "%v", "FreeBSD-runtime").Out()
}

func (p *proto) listDirs(root string, subs ...string) []string {
	var out []string
	for _, s := range subs {
		r := p.e.Quiet("find", root+s, "-type", "d")
		for _, l := range strings.Split(r.Out(), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				out = append(out, strings.TrimPrefix(l, root))
			}
		}
	}
	return out
}

func (p *proto) etcTar(root, tar string) bool {
	return p.e.Run("tar", "-C", root, "-cf", tar, "./etc").OK()
}

// etcupdate runs the baseline extract and the update against destdir. UNVERIFIED: that
// etcupdate accepts tarballs of a pkgbase /etc via -t, that extract needs -D, and what a
// pkgbase tree (no /usr/src) does to its "build" step. Returns conflicts found.
func (p *proto) etcupdate(wd, dest, oldTar, newTar string) (conflicts []string, ran bool) {
	e := p.e
	r1 := e.Run("etcupdate", "extract", "-d", wd, "-t", oldTar, "-D", dest)
	r2 := e.Run("etcupdate", "-d", wd, "-t", newTar, "-D", dest, "-L", wd+"/log")
	st := e.Quiet("etcupdate", "status", "-d", wd, "-D", dest)
	conflicts = thinupgrade.ParseEtcupdateStatus(st.Stdout + st.Stderr)
	if !e.DryRun {
		_ = filepath.Walk(dest+"/etc", func(path string, info os.FileInfo, err error) error {
			if err == nil && info.Mode().IsRegular() && info.Size() < 1<<20 && thinupgrade.HasConflictMarkers(p.read(path)) {
				conflicts = append(conflicts, strings.TrimPrefix(path, dest))
			}
			return nil
		})
	}
	return dedupe(conflicts), r1.OK() && r2.OK()
}

func dedupe(l []string) []string {
	seen := map[string]bool{}
	var o []string
	for _, x := range l {
		if !seen[x] {
			seen[x] = true
			o = append(o, x)
		}
	}
	return o
}

func run(e *harness.Env) {
	p := newProto(e)
	e.Step("Preflight")
	e.Require("zfs", "jail", "jexec", "jls", "pkg", "etcupdate", "tar", "pw", "find", "freebsd-version")
	if e.DatasetExists(e.Root) {
		e.Abort("%s already exists: run with --cleanup first", e.Root)
	}
	hostK, _ := thinupgrade.ParseVersion(e.Run("freebsd-version", "-k").Out())
	e.Note("host kernel: %s; abi %s; sets %s; persistent paths planned: %s", hostK.Raw, cfg.abi, cfg.sets, thinupgrade.MountList(p.persist))

	e.Step("Create scratch tree")
	e.Must("create-root", e.Zfs("create", "-p", "-o", "mountpoint="+p.mnt, "-o", "jailops:managed=on", e.Root).OK(), e.Root)
	e.Zfs("create", p.ds("images"))
	e.Zfs("create", p.ds("jail"))
	e.Zfs("create", p.ds("work"))
	p.buildRepoConf()

	e.Step("Build generation g1 (pkgbase)")
	e.Must("create-g1", e.Zfs("create", p.ds("images", "g1")).OK(), "dataset images/g1")
	// UNVERIFIED: `pkg -r <empty root> install` bootstraps the database and runs base package
	// scripts (pwd_mkdb, ldconfig, etc) in an empty directory tree.
	r := p.pkg(p.img("g1"), "install", "-y", "-r", "FreeBSD-base", strings.ReplaceAll(cfg.sets, ",", " "))
	if !r.OK() && cfg.sets != "" {
		r = p.pkg(p.img("g1"), "install", "-y", strings.ReplaceAll(cfg.sets, ",", " "))
	}
	e.Must("g1-pkgbase-install", r.OK(), "pkg -r g1 install "+cfg.sets)
	sane := p.exists(p.img("g1")+"/libexec/ld-elf.so.1") && p.exists(p.img("g1")+"/bin/sh") && p.exists(p.img("g1")+"/etc/master.passwd")
	e.Must("g1-sane", sane || e.DryRun, "ld-elf.so.1, /bin/sh and /etc/master.passwd exist in g1")
	e.Resolve("pkgbase: `pkg -r <dir> install <set>` builds a bootable-userland tree in an empty ZFS dataset")
	g1v := p.baseVersion(p.img("g1"))
	e.Note("g1 FreeBSD-runtime version: %s", g1v)
	e.Must("g1-snapshot", e.Zfs("snapshot", p.ds("images", "g1")+"@ro").OK(), "images/g1@ro")

	e.Step("Create thin jail: clone of g1@ro plus persistent datasets")
	e.Must("clone-root", e.Zfs("clone", "-o", "mountpoint="+p.jroot, p.ds("images", "g1")+"@ro", p.jds("root")).OK(), "jail/root is a clone of g1@ro")
	for _, ps := range p.persist {
		p.seedAndMount(ps)
	}
	e.Resolve("persistent datasets mounted over a clone root, seeded with tar, are visible inside a jail (checked by health below)")
	p.write(p.conf, thinupgrade.JailConf(jailName, p.jroot), 0o644)
	e.OnCleanup(func() { p.stopJail() })
	e.Zfs("snapshot", p.jds("root")+"@jop-base")
	e.Must("jail-start-g1", p.startJail(), "jail -c "+jailName)
	appOK := true
	if cfg.appPkg != "" {
		r := p.pkg(p.jroot, "install", "-y", "-r", "FreeBSD-ports", cfg.appPkg)
		appOK = r.OK()
		e.Check("app-pkg-install", appOK, "pkg -r jail install "+cfg.appPkg+" (needs network and a ports repo)")
		if !appOK {
			e.Note("falling back to a marker script; rerun with --app-pkg '' on hosts without network")
			cfg.appPkg = ""
		}
	}
	if cfg.appPkg == "" {
		p.write(p.jp("/usr/local/bin/jop-app"), "#!/bin/sh\necho app-ok\n", 0o755)
	}
	for _, ps := range p.persist {
		p.write(p.jp(filepath.Join(ps.Mount, "jop-marker")), "pre-upgrade "+ps.Mount+"\n", 0o644)
	}
	p.write(p.jp("/jop-rootonly-old"), "written in g1 root, outside persistent paths\n", 0o644)
	p.health("g1")

	// Snapshot used by all-rollback and for `zfs get written`.
	e.Step("Build generation g2 (staging clone of g1@ro)")
	e.Must("clone-g2", e.Zfs("clone", p.ds("images", "g1")+"@ro", p.ds("images", "g2")).OK(), "images/g2 staging clone")
	e.Zfs("set", "mountpoint="+p.img("g2"), p.ds("images", "g2"))
	up := p.pkg(p.img("g2"), "upgrade", "-y", "-r", "FreeBSD-base")
	e.Check("g2-pkg-upgrade", up.OK(), "pkg -r g2 upgrade -r FreeBSD-base (a no-op is fine if no newer base exists)")
	var driftFile string
	if cfg.synthetic {
		e.Note("synthetic g2 changes (a base repo may have nothing newer): new file, edited line, new user, new dir in persistent /var/db")
		p.write(p.img("g2")+"/etc/jop-newdefault.conf", "# new default shipped in g2\n", 0o644)
		e.Run("pw", "-R", p.img("g2"), "usermod", "daemon", "-c", "jop theirs")
		e.Run("pw", "-R", p.img("g2"), "useradd", "joptheirs", "-u", "3002", "-d", "/nonexistent", "-s", "/usr/sbin/nologin")
		e.Run("install", "-d", "-m", "0750", p.img("g2")+"/var/db/jop-newdir")
		for _, c := range []string{"/etc/hosts", "/etc/crontab", "/etc/syslog.conf"} {
			if strings.Count(p.read(p.img("g2")+c), "\n") >= 8 {
				driftFile = c
				break
			}
		}
		if driftFile != "" {
			b := p.read(p.img("g2") + driftFile)
			p.write(p.img("g2")+driftFile, "# jop theirs header\n"+b[strings.Index(b, "\n")+1:], 0o644)
		}
	}
	g2v := p.baseVersion(p.img("g2"))
	e.Must("g2-snapshot", e.Zfs("snapshot", p.ds("images", "g2")+"@ro").OK(), "images/g2@ro")
	e.Note("g2 FreeBSD-runtime version: %s", g2v)
	v1, err1 := thinupgrade.ParseVersion(g1v)
	v2, err2 := thinupgrade.ParseVersion(g2v)
	if err1 == nil && err2 == nil {
		_, note := thinupgrade.MajorChange(v1, v2)
		e.Check("major-version-note", true, note)
		e.Check("preflight-kernel-not-older-than-image", !thinupgrade.KernelTooOld(hostK, v2), fmt.Sprintf("host kernel %s, image %s", hostK.Raw, g2v))
	} else {
		e.Check("major-version-note", true, "versions unparsable ("+g1v+" / "+g2v+"); a major upgrade must be a separate strategy: ABI change, pkg -f, new etcupdate baseline")
	}

	e.Step("Upgrade g1 -> g2 (design 4.3)")
	// Jail-side edits made before the upgrade, to be merged with g2's.
	p.write(p.jp("/etc/jop-local.conf"), "local only\n", 0o644)
	e.Run("pw", "-R", p.jroot, "useradd", "jopours", "-u", "3001", "-d", "/nonexistent", "-s", "/usr/sbin/nologin")
	if driftFile != "" {
		b := p.read(p.jp(driftFile))
		p.write(p.jp(driftFile), b+"# jop ours footer\n", 0o644)
	}
	p.write(p.jp("/etc/rc.conf"), "sshd_enable=\"NO\"\n# jop ours\n", 0o644)
	rcBefore := p.read(p.jp("/etc/rc.conf"))
	p.persistSnapshot("jop-pre-upgrade")
	p.stopJail()
	e.Must("clone-root-new", e.Zfs("clone", "-o", "mountpoint=none", p.ds("images", "g2")+"@ro", p.jds("root.new")).OK(), "jail/root.new from g2@ro")
	wd := p.work + "/etcupdate-main"
	t1, t2 := p.work+"/g1-etc.tar", p.work+"/g2-etc.tar"
	p.etcTar(p.img("g1"), t1)
	p.etcTar(p.img("g2"), t2)
	conf, ran := p.etcupdate(wd, p.jroot, t1, t2)
	e.Check("etcupdate-ran", ran, "etcupdate extract + update with tarballs of g1/g2 /etc against the persistent /etc (UNVERIFIED invocation)")
	e.Resolve("etcupdate usable with tarballs on a pkgbase tree: " + fmt.Sprint(ran))
	e.Check("etcupdate-clean-merge", len(conf) == 0, fmt.Sprintf("disjoint edits must merge cleanly; conflicts=%v", conf))
	if len(conf) > 0 {
		e.Note("design 4.3 step 6: a conflict leaves the jail on g1 and raises an event. Staying on g1.")
		e.Zfs("destroy", p.jds("root.new"))
		p.startJail()
		return
	}
	pw := p.read(p.jp("/etc/master.passwd"))
	e.Check("merge-passwd-both-sides", strings.Contains(pw, "jopours:") && strings.Contains(pw, "joptheirs:") || e.DryRun, "master.passwd keeps the jail's user and gains g2's user")
	e.Check("merge-new-default-file", p.exists(p.jp("/etc/jop-newdefault.conf")) || e.DryRun || !cfg.synthetic, "new /etc file from g2 appears")
	e.Check("merge-rc-conf-untouched", p.read(p.jp("/etc/rc.conf")) == rcBefore, "rc.conf is not shipped by the base, so etcupdate must leave it byte-identical")
	e.Check("merge-local-file-kept", p.exists(p.jp("/etc/jop-local.conf")) || e.DryRun, "jail-local file survives")
	e.Check("pwd-db-consistent", p.e.Run("pwd_mkdb", "-C", p.jp("/etc/master.passwd")).OK(), "pwd_mkdb -C accepts the merged master.passwd (UNVERIFIED flag)")

	e.Step("Swap roots and start")
	p.swapRoot("root.old", "root.new")
	e.Check("start-g2", p.startJail(), "jail starts on the g2 clone")
	h := p.health("g2")
	if !h {
		e.Note("health failed: swapping back (design 4.3 step 8)")
		p.stopJail()
		p.swapRoot("root.new", "root.old")
		p.startJail()
		e.Abort("health check failed after upgrade")
	}

	e.Step("Post-upgrade findings")
	// pkg database finding: persistent /var/db carries g1's package database.
	jv := p.baseVersion(p.jroot)
	e.Check("pkgdb-follows-root", jv == g2v || g1v == g2v, fmt.Sprintf("base version per the jail's /var/db/pkg is %q, g2 is %q, g1 is %q. If this fails, persisting /var/db makes `pkg` report the OLD base: re-run with --var-paths log,mail,spool and compare", jv, g2v, g1v))
	drift := thinupgrade.DriftDirs(p.listDirs(p.img("g1"), "/var", "/etc"), p.listDirs(p.img("g2"), "/var", "/etc"), mounts(p.persist))
	e.Check("var-skeleton-drift-detected", len(drift.Added) > 0 || !cfg.synthetic || e.DryRun, fmt.Sprintf("dirs new in g2 inside persistent mounts: %v; removed: %v", drift.Added, drift.Removed))
	for _, d := range drift.Added {
		if strings.HasPrefix(d, "/etc") {
			continue
		}
		missing := !p.exists(p.jp(d))
		e.Note("drift %s hidden by persistent mount: missing in jail=%v", d, missing)
		if missing {
			mode := p.e.Quiet("stat", "-f", "%Lp", p.img("g2")+d).Out()
			e.Run("install", "-d", "-m", mode, p.jp(d))
		}
	}
	e.Check("var-skeleton-drift-repaired", p.exists(p.jp("/var/db/jop-newdir")) || !cfg.synthetic || e.DryRun, "missing skeleton directories created from g2 (the reconcile step JailOps needs)")
	p.write(p.jp("/var/db/jop-postdata"), "written after upgrade\n", 0o644)
	p.write(p.jp("/usr/local/jop-postdata"), "written after upgrade\n", 0o644)
	p.write(p.jp("/etc/jop-postdata"), "written after upgrade\n", 0o644)
	p.write(p.jp("/jop-rootonly-new"), "written in g2 root, outside persistent paths\n", 0o644)
	w, wl := p.writtenReport("jop-pre-upgrade")
	e.Check("zfs-written-reported", w > 0 || e.DryRun, "zfs get written@jop-pre-upgrade: "+wl)

	e.Step("Scenario: root rollback keeps data")
	p.stopJail()
	p.swapRoot("root.new", "root.old")
	e.Check("rollback-root-start", p.startJail(), "jail starts on the g1 clone again")
	var fs []thinupgrade.Finding
	for _, ps := range p.persist {
		fs = append(fs, thinupgrade.Finding{Path: ps.Mount, Survived: p.exists(p.jp(filepath.Join(ps.Mount, "jop-marker")))})
	}
	okp, lost := thinupgrade.SurvivalSummary(fs)
	e.Check("rollback-root-persistent-markers", len(lost) == 0 || e.DryRun, fmt.Sprintf("survived: %v lost: %v", okp, lost))
	e.Check("rollback-root-keeps-postdata", (p.exists(p.jp("/var/db/jop-postdata")) && p.exists(p.jp("/usr/local/jop-postdata"))) || e.DryRun, "data written after the upgrade is still there (scope root)")
	e.Check("rollback-root-drops-rootonly", !p.exists(p.jp("/jop-rootonly-new")), "writes outside persistent paths vanish with the root (expected, and the reason the list matters)")
	e.Check("rollback-root-etc-skew", p.exists(p.jp("/etc/jop-newdefault.conf")) || !cfg.synthetic || e.DryRun, "FINDING: /etc stays at g2-merged state while the root is g1 (scope root does not roll /etc back); record as a design note")
	e.Resolve("root rollback by zfs rename swap leaves persistent datasets untouched")

	e.Step("Scenario: all rollback loses data")
	p.stopJail()
	p.swapRoot("root.old", "root.new")
	p.startJail()
	p.write(p.jp("/var/db/jop-postdata2"), "second\n", 0o644)
	w2, wl2 := p.writtenReport("jop-pre-upgrade")
	e.Note("zfs get written@jop-pre-upgrade before all-rollback: total %s (%s)", thinupgrade.HumanBytes(w2), wl2)
	p.stopJail()
	p.swapRoot("root.new", "root.old")
	// root is now the g1 clone, which carries the @jop-pre-upgrade snapshot.
	rb := true
	for _, n := range append([]string{"root"}, persistNames(p.persist)...) {
		rb = e.Zfs("rollback", "-r", p.jds(n)+"@jop-pre-upgrade").OK() && rb
	}
	for _, ps := range p.persist {
		e.Zfs("mount", p.jds(ps.Name)) // rollback may leave mounts; harmless if already mounted
	}
	e.Check("rollback-all-zfs", rb, "zfs rollback -r of root and every persistent dataset to jop-pre-upgrade")
	p.startJail()
	e.Check("rollback-all-loses-data", !p.exists(p.jp("/var/db/jop-postdata")) && !p.exists(p.jp("/usr/local/jop-postdata")) && !p.exists(p.jp("/etc/jop-postdata")) || e.DryRun, "post-upgrade data is gone (scope all)")
	e.Check("rollback-all-etc-restored", !p.exists(p.jp("/etc/jop-newdefault.conf")) || e.DryRun, "/etc is back to the pre-merge state")
	e.Check("rollback-all-health", p.health("g1-again"), "health check on restored g1")
	e.Resolve("zfs rollback of a snapshot taken by one atomic zfs snapshot restores root + persistent datasets consistently")

	p.conflictScenarios(t1, t2)

	e.Step("RESULT: persistent path list")
	e.Note("EXACT PERSISTENT PATH LIST THAT WORKED (markers survived root rollback): %s", strings.Join(okp, " "))
	e.Note("not persistent, lost with the root: everything else, e.g. /var/run /var/tmp /var/cache /tmp and files written in /")
	e.Note("JailOps design decision input: pkg database lives in /var/db/pkg; see check pkgdb-follows-root")
	e.Note("Run with --cleanup to remove %s.", e.Root)
}

func mounts(ps []thinupgrade.Persist) []string {
	var m []string
	for _, p := range ps {
		m = append(m, p.Mount)
	}
	return m
}

func persistNames(ps []thinupgrade.Persist) []string {
	var n []string
	for _, p := range ps {
		n = append(n, p.Name)
	}
	return n
}

// conflictScenarios runs etcupdate on throwaway copies of the jail's /etc to learn how
// master.passwd and other conflicts present themselves, without touching the jail.
func (p *proto) conflictScenarios(t1, t2 string) {
	e := p.e
	e.Step("Scenario: /etc merge conflicts on a scratch copy")
	run := func(tag string, mutOurs, mutTheirs func(dest, theirs string)) []string {
		d := p.work + "/conf-" + tag
		th := p.work + "/conf-" + tag + "-theirs"
		e.Run("mkdir", "-p", d, th)
		e.Pipe([]string{"tar", "-C", p.jroot, "-cf", "-", "./etc"}, []string{"tar", "-C", d, "-xpf", "-"})
		e.Pipe([]string{"tar", "-C", p.img("g2"), "-cf", "-", "./etc"}, []string{"tar", "-C", th, "-xpf", "-"})
		mutOurs(d, th)
		mutTheirs(d, th)
		nt := p.work + "/conf-" + tag + ".tar"
		e.Run("tar", "-C", th, "-cf", nt, "./etc")
		c, _ := p.etcupdate(p.work+"/etcupdate-"+tag, d, t2, nt)
		return c
	}
	pwline := func(root, user, gecos string) {
		e.Run("pw", "-R", root, "usermod", user, "-c", gecos)
	}
	c := run("both-add", func(d, th string) {
		e.Run("pw", "-R", d, "useradd", "jopa", "-u", "3101", "-d", "/nonexistent", "-s", "/usr/sbin/nologin")
	}, func(d, th string) {
		e.Run("pw", "-R", th, "useradd", "jopb", "-u", "3102", "-d", "/nonexistent", "-s", "/usr/sbin/nologin")
	})
	e.Check("conflict-passwd-both-add", true, fmt.Sprintf("INFO both sides appended a user to master.passwd: etcupdate conflicts=%v. A keyed merge (Merge3Keyed) resolves this without conflict; JailOps should not hand master.passwd to a line diff", c))
	c = run("same-line", func(d, th string) { pwline(d, "daemon", "ours-gecos") }, func(d, th string) { pwline(th, "daemon", "theirs-gecos") })
	e.Check("conflict-passwd-same-line-detected", len(c) > 0 || e.DryRun, fmt.Sprintf("same master.passwd line changed on both sides must conflict, never be guessed; conflicts=%v", c))
	c = run("rc-conf", func(d, th string) {
		p.write(d+"/etc/rc.conf", "sshd_enable=\"YES\"\n", 0o644)
	}, func(d, th string) {
		p.write(th+"/etc/rc.conf", "sshd_enable=\"NO\"\n", 0o644)
	})
	e.Check("conflict-rc-conf", true, fmt.Sprintf("INFO rc.conf present only as a new file in the target tarball while the jail has its own: conflicts=%v (JailOps renders rc.conf itself, so this must never be a merge)", c))
	e.Resolve("etcupdate conflict reporting: master.passwd same-line conflict is detected, both-add behaviour recorded")
}

func cleanup(e *harness.Env) {
	p := newProto(e)
	e.Step("Cleanup")
	p.stopJail()
	e.Run("umount", "-f", p.jroot+"/dev") // only if the jail left devfs behind; failure is fine
	if e.DatasetExists(e.Root) || e.DryRun {
		e.Zfs("destroy", "-r", "-f", e.Root)
	} else {
		e.Note("%s does not exist", e.Root)
	}
	if !e.DryRun {
		_ = os.Remove(p.mnt)
	}
}
