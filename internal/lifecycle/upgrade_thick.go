package lifecycle

import (
	"bytes"
	"context"
	"debug/elf"
	"errors"
	"fmt"
	"path"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"jailops.net/jailops/internal/imagegen"
	"jailops.net/jailops/internal/plan"
	"jailops.net/jailops/sdk/api"
)

// suspectedPrefix is how the helper starts the refusal of a root its preflight detection found prepared against
// host-side pkg (priv.SuspectedPrefix).
const suspectedPrefix = "suspected_compromised: "

// SuspectedError is the refusal of a jail whose root the preflight detection found prepared against host-side pkg: an
// indicator that the jail is compromised, of high severity. The jail is left alone and the run goes on with the others.
type SuspectedError struct {
	Jail, Found string
	// During says the finding came from a pkg step of the upgrade, after the check: the root may be partly upgraded.
	During bool
}

func (e *SuspectedError) Error() string {
	if e.During {
		return "suspected_compromised: jail " + e.Jail + ": " + e.Found + "; found during the upgrade, which stopped there: the snapshot of the upgrade is kept"
	}
	return "suspected_compromised: jail " + e.Jail + ": " + e.Found + "; it is not upgraded, and nothing in it was changed"
}

// Unwrap gives the API's form of the refusal: forbidden, with what to do.
func (e *SuspectedError) Unwrap() error {
	return api.Errorf(api.CodeForbidden, "%s", e.Error()).
		WithHint("look at the jail: a link out of the root on a path pkg writes, or among pkg's own files, is what an attack on host-side pkg plants; remove it, or make it point inside the jail, and the next run checks again")
}

// suspectedFinding is what follows the prefix in the helper's refusal.
func suspectedFinding(msg string) string {
	i := strings.Index(msg, suspectedPrefix)
	if i < 0 {
		return strings.TrimSpace(msg)
	}
	f := msg[i+len(suspectedPrefix):]
	if j := strings.Index(f, "\n"); j >= 0 {
		f = f[:j]
	}
	return strings.TrimSpace(f)
}

// checkSeq tells the scratch directories of checks made in the same second apart.
var checkSeq atomic.Int64

// upgradeSnapPrefix names the snapshot an in-place upgrade takes of the jail; CleanupRootOld removes it once its
// cleanup mark is due.
const upgradeSnapPrefix = "jo-pre-upgrade-"

// reUserland reads the version a FreeBSD userland carries in /bin/freebsd-version.
var reUserland = regexp.MustCompile(`(?m)^USERLAND_VERSION="([^"]+)"`)

// thickRelease is a release string split: Rel without the patch level ("15.1-RELEASE"), Full with it
// ("15.1-RELEASE-p7").
type thickRelease struct{ Rel, Full string }

// releaseSpellings: which release strings `--release` accepts. RELEASE reads base_release_<minor>; every other type
// (STABLE, CURRENT, PRERELEASE, BETA, RC, ALPHA) reads base_latest.
var reReleaseArg = regexp.MustCompile(`^[0-9]+\.[0-9]+-(RELEASE|STABLE|CURRENT|PRERELEASE|(BETA|RC|ALPHA)[0-9]+)$`)

// installedRelease is the release installed in a jail's root: the userland version of /bin/freebsd-version (it
// carries the type and the patch level), else the version of the FreeBSD-runtime package. The root is read, not
// entered, so a stopped jail answers too.
func (s *Service) installedRelease(ctx context.Context, name string, script []byte, runtimeVersion string) (thickRelease, error) {
	if len(script) > 0 {
		if m := reUserland.FindSubmatch(script); m != nil {
			full := string(m[1])
			rel := full
			if i := strings.LastIndex(full, "-p"); i > 0 {
				if _, err := strconv.Atoi(full[i+2:]); err == nil {
					rel = full[:i]
				}
			}
			if _, _, err := parseRel(rel); err == nil {
				return thickRelease{Rel: rel, Full: full}, nil
			}
		}
	}
	// the package version: 15.1p7 is RELEASE patch 7; anything else is a snapshot of a branch
	v := runtimeVersion
	var ma, mi int
	var rest string
	if n, _ := fmt.Sscanf(v, "%d.%d", &ma, &mi); n == 2 {
		rest = strings.TrimPrefix(v, fmt.Sprintf("%d.%d", ma, mi))
		if rest == "" || strings.HasPrefix(rest, "p") || strings.HasPrefix(rest, "_") {
			return thickRelease{Rel: fmt.Sprintf("%d.%d-RELEASE", ma, mi), Full: fmt.Sprintf("%d.%d-RELEASE", ma, mi) + patchOf(rest)}, nil
		}
		return thickRelease{Rel: fmt.Sprintf("%d.%d-CURRENT", ma, mi), Full: v}, nil
	}
	return thickRelease{}, api.Errorf(api.CodeInternal, "cannot tell the release installed in jail %s (FreeBSD-runtime %q)", plainName(name), v)
}

func patchOf(rest string) string {
	if strings.HasPrefix(rest, "p") {
		if n, err := strconv.Atoi(strings.SplitN(rest[1:], "_", 2)[0]); err == nil {
			return "-p" + strconv.Itoa(n)
		}
	}
	return ""
}

// thickArch is the architecture of a jail: the machine of the root's /bin/sh (the root is mounted), else the
// host's. The image the jail was made from plays no part.
func (s *Service) thickArch(ctx context.Context, st *jailState) string {
	if b, err := s.Files.ReadFile(ctx, path.Join(s.Cfg.at(st.Name).rootMP(), "bin/sh")); err == nil {
		if f, err := elf.NewFile(bytes.NewReader(b)); err == nil {
			switch f.Machine {
			case elf.EM_X86_64:
				return "amd64"
			case elf.EM_AARCH64:
				return "arm64"
			}
		}
	}
	if runtime.GOARCH == "arm64" {
		return "arm64"
	}
	return "amd64"
}

// thickPkgArgv is `pkg -r <root>` for the base of a jail: the repository configuration and the cache outside the
// root, the ABI of the release, the OS version of the host's kernel (hostOSVersion), and no package script (RUN_SCRIPTS=false): the scripts of the base
// packages run the host's programs on paths of the root, which its tenant controls, so they are not run from the host;
// what they do is done inside the started jail (settleStep). The helper adds an empty configuration file of its own
// (-C) and turns plugins off, so nothing of the root configures this pkg.
func thickPkgArgv(root, reposDir, cacheDir, abi, osversion string, args ...string) []string {
	base := []string{"pkg", "-r", root, "-o", "ABI=" + abi, "-o", "OSVERSION=" + osversion, "-o", "REPOS_DIR=" + reposDir, "-o", "PKG_CACHEDIR=" + cacheDir, "-o", "RUN_SCRIPTS=false"}
	return append(base, args...)
}

// hostOSVersion is the OSVERSION pkg is given for a root of FreeBSD major release major: the __FreeBSD_version of the
// host's kernel (kern.osreldate), held to that major. pkg refuses a repository whose packages were built for a newer
// OSVERSION (verified with pkg 2.9.2: "repository jailops contains packages for wrong OS version"), so a jail never gets
// a userland newer than the kernel it runs on. pkg also takes the major of its ABI from OSVERSION (verified: ABI
// FreeBSD:15 with OSVERSION 1600031 reads the FreeBSD:16 repository), so a 15 jail on a 16 host gets the newest 15
// (1599999), never more. Without the runtime's answer it is the newest version of the host's release.
func (s *Service) hostOSVersion(ctx context.Context, host string, major int) string {
	n := 0
	if h, ok := s.Jails.(interface {
		HostOSVersion(context.Context) (int, error)
	}); ok {
		if v, err := h.HostOSVersion(ctx); err == nil && v > 0 {
			n = v
		}
	}
	if n == 0 {
		a, b, err := parseRel(host)
		if err != nil {
			a, b = major, 0
		}
		n = a*100000 + b*1000 + 999
	}
	return strconv.Itoa(min(n, major*100000+99999))
}

// copyPackageDB copies the package database of a jail's root (var/db/pkg) into the scratch root work/scratch. pkg
// reads the copy, never the jail's own files: the checks, the dry run and the list of base packages run pkg as root of
// the host, and the database of the root is its tenant's. The catalogues of the jail's repositories are removed from
// the copy. The root is mounted. A symbolic link in the copy is refused by the helper when pkg reads it.
func (s *Service) copyPackageDB(ctx context.Context, st *jailState, work string) error {
	dst := work + "/scratch/var/db/pkg"
	if err := s.Files.MkdirAll(ctx, dst); err != nil {
		return err
	}
	src := path.Join(s.Cfg.at(st.Name).rootMP(), "var/db/pkg")
	if ok, err := s.Files.Exists(ctx, src); err != nil {
		return err
	} else if !ok {
		return api.Errorf(api.CodePreconditionFailed, "jail %s has no package database (/var/db/pkg): its base is not one pkg installed", plainName(st.Name))
	}
	if err := s.Files.CopyTree(ctx, src, dst); err != nil {
		return fmt.Errorf("copy the package database of jail %s: %w", plainName(st.Name), err)
	}
	// the catalogues of the jail's repositories are not read, and an old catalogue of the base repository (the image's,
	// or a former upgrade's) would make pkg refuse the repository of another release
	return s.Files.RemoveAll(ctx, dst+"/repos")
}

// checkRoot runs the helper's check of the root of a running jail on the paths its base packages own, read from the copy
// of its package database in checkDir/scratch (installedState).
func (s *Service) checkRoot(ctx context.Context, st *jailState, checkDir string, installed map[string]pkgInfo) error {
	var base []string
	for n := range installed {
		if strings.HasPrefix(n, "FreeBSD-") {
			base = append(base, n)
		}
	}
	sort.Strings(base)
	var paths []string
	for len(base) > 0 { // the helper takes at most 64 names in one pkg command
		k := min(len(base), 60)
		out, err := s.Exec.Run(ctx, append([]string{"pkg", "-r", checkDir + "/scratch", "query", "%Fp"}, base[:k]...))
		if err != nil {
			return fmt.Errorf("read the files of the base packages of jail %s: %w: %s", plainName(st.Name), err, tailLine(string(out)))
		}
		for _, l := range strings.Split(string(out), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				paths = append(paths, l)
			}
		}
		base = base[k:]
	}
	if err := s.Files.CheckRoot(ctx, s.Cfg.at(st.Name).rootMP(), paths); err != nil {
		if strings.Contains(err.Error(), suspectedPrefix) {
			return &SuspectedError{Jail: plainName(st.Name), Found: suspectedFinding(err.Error())}
		}
		return fmt.Errorf("check the root of jail %s: %w", plainName(st.Name), err)
	}
	return nil
}

// installedState reads what a jail has installed: the packages, from a copy of its package database in the
// scratch root work/scratch (copyPackageDB), the text of its /bin/freebsd-version (the userland version it carries) and
// its architecture. A stopped jail's root is mounted for the read and unmounted again. The jail is not changed.
func (s *Service) installedState(ctx context.Context, st *jailState, running bool, work string) (map[string]pkgInfo, []byte, string, error) {
	// a stopped jail's root is mounted for the read and unmounted after it; a root that is mounted already (an
	// operation has it) is read as it is and left mounted
	if !running && s.prop(ctx, st.Root, "mounted") != "yes" {
		defer func() { _ = s.unmountTree(context.WithoutCancel(ctx), st.Name) }()
		if err := s.mountTree(ctx, st.Name); err != nil {
			return nil, nil, "", err
		}
	}
	if err := s.copyPackageDB(ctx, st, work); err != nil {
		return nil, nil, "", err
	}
	// a query reads the local database only: the ABI and the repositories play no part
	out, err := s.Exec.Run(ctx, []string{"pkg", "-r", work + "/scratch", "query", "%n %v %sb"})
	if err != nil {
		return nil, nil, "", fmt.Errorf("read the packages installed in the jail: %w: %s", err, strings.TrimSpace(string(out)))
	}
	script, _ := s.Files.ReadFile(ctx, path.Join(s.Cfg.at(st.Name).rootMP(), "bin/freebsd-version"))
	return parsePackages(string(out)), script, s.thickArch(ctx, st), nil
}

// BasePackages lists the base packages (FreeBSD-*) installed in a jail, running or stopped, as name and version.
// They are read from a copy of the jail's package database (installedState).
func (s *Service) BasePackages(ctx context.Context, name string) ([][2]string, error) {
	st, err := s.ready(ctx, name)
	if err != nil {
		return nil, err
	}
	rec, err := s.readRecord(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("read spec.json: %w", err)
	}
	if err := st.refuseUnsupported(); err != nil {
		return nil, err
	}
	if rec.Spec.OS == api.OSLinux {
		return nil, api.Errorf(api.CodeUnsupported, "base packages are listed for FreeBSD jails")
	}
	running, err := s.Jails.Running(ctx, name)
	if err != nil {
		return nil, err
	}
	work := fmt.Sprintf("%s/.upgrade-%s-list%d", s.Cfg.at(name).containerMP(), s.stamp(""), checkSeq.Add(1))
	defer func() { _ = s.Files.RemoveAll(context.WithoutCancel(ctx), work) }()
	m, _, _, err := s.installedState(ctx, st, running, work)
	if err != nil {
		return nil, err
	}
	var out [][2]string
	for n, v := range m {
		if strings.HasPrefix(n, "FreeBSD-") {
			out = append(out, [2]string{n, v.Version})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out, nil
}

// catalogue reads the repository catalogue of the release into the scratch root work/scratch (the repository
// configuration is outside it) and returns the name and version of every package in it. work is removed by the caller.
func (s *Service) catalogue(ctx context.Context, work, abi, osver, repoURL, repoBody, cacheDir string) (map[string]pkgInfo, error) {
	repos, scratch := work+"/repos", work+"/scratch"
	for _, d := range []string{repos, scratch, cacheDir} {
		if err := s.Files.MkdirAll(ctx, d); err != nil {
			return nil, err
		}
	}
	if err := s.Files.WriteAtomic(ctx, repos+"/"+imagegen.PkgBaseRepoName+".conf", []byte(repoBody)); err != nil {
		return nil, err
	}
	// the keys the repository is verified with come from the host
	if sk, ok := s.Files.(interface {
		SeedKeys(context.Context, string) error
	}); ok {
		if err := sk.SeedKeys(ctx, scratch); err != nil {
			return nil, err
		}
	}
	if err := imagegen.RunPkg(ctx, s.Exec.Run, thickPkgArgv(scratch, repos, cacheDir, abi, osver, "update", "-f", "-r", imagegen.PkgBaseRepoName)); err != nil {
		if strings.Contains(err.Error(), "wrong OS version") {
			return nil, &causedError{api.Errorf(api.CodeVersionSkew, "the repository %s has packages built for a newer FreeBSD than the host kernel (kern.osreldate %s): a jail's userland is not newer than the kernel it runs on", repoURL, osver).
				WithHint("upgrade the host first"), err}
		}
		return nil, &causedError{api.Errorf(api.CodePreconditionFailed, "the repository %s cannot be read: %s", repoURL, tailLine(err.Error())).
			WithHint("check the host's network and DNS, that the release has a repository (a RELEASE that does not exist yet answers 404), and the keys under /usr/share/keys"), err}
	}
	out, err := s.Exec.Run(ctx, thickPkgArgv(scratch, repos, cacheDir, abi, osver, "rquery", "-r", imagegen.PkgBaseRepoName, "%n %v %sb"))
	if err != nil {
		return nil, fmt.Errorf("read the repository catalogue: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return parsePackages(string(out)), nil
}

// pkgInfo is a package as pkg lists it: its version and its flat size (what it takes on disk once installed).
type pkgInfo struct {
	Version string
	Size    int64
}

// parsePackages reads the lines "name version flatsize" of pkg query and pkg rquery.
func parsePackages(out string) map[string]pkgInfo {
	m := map[string]pkgInfo{}
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) < 2 {
			continue
		}
		p := pkgInfo{Version: f[1]}
		if len(f) >= 3 {
			p.Size, _ = strconv.ParseInt(f[2], 10, 64)
		}
		m[f[0]] = p
	}
	return m
}

// pkgChange is one package of pkg's own plan: its name, the version installed (empty for a new one) and the version
// it gets (empty for one pkg removes).
type pkgChange struct {
	Name, From, To, Kind string
}

func (c pkgChange) String() string {
	switch c.Kind {
	case "install":
		return c.Name + " (new) " + c.To
	case "remove":
		return c.Name + " " + c.From + " (removed)"
	case "reinstall":
		return c.Name + " " + c.To + " (reinstalled)"
	}
	return c.Name + " " + c.From + " -> " + c.To
}

// pkgPlan is what `pkg upgrade -n` or `pkg install -n` reports: the packages it would change, the bytes it would
// download (0 when it does not say: they are in the cache, or pkg did not print it) and how much more (or, negative,
// less) room the packages take once installed.
type pkgPlan struct {
	Changes        []pkgChange
	Download, Size int64
}

var (
	reSizeLine  = regexp.MustCompile(`([0-9.]+) ?([KMGT]i?B|B) (more space|to be downloaded)|will free ([0-9.]+) ?([KMGT]i?B|B)`)
	rePkgHeader = regexp.MustCompile(`^(New packages to be INSTALLED|Installed packages to be (UPGRADED|DOWNGRADED|REINSTALLED|REMOVED)):$`)
)

// parsePkgPlan reads the dry run of pkg 2.x. An answer that says nothing pkg would change and is not one of pkg's
// "nothing to do" answers is an error: the decision is pkg's, never guessed.
func parsePkgPlan(out string) (pkgPlan, error) {
	var p pkgPlan
	kind := ""
	nothing := false
	unit := map[string]float64{"B": 1, "KiB": 1 << 10, "KB": 1 << 10, "MiB": 1 << 20, "MB": 1 << 20, "GiB": 1 << 30, "GB": 1 << 30, "TiB": 1 << 40, "TB": 1 << 40}
	for _, raw := range strings.Split(imagegen.CleanOutput(out), "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case line == "":
			kind = ""
			continue
		case strings.Contains(line, "Your packages are up to date"), strings.Contains(line, "most recent versions of packages are already installed"),
			strings.Contains(line, "(0 candidates)"):
			nothing = true
			continue
		}
		if m := rePkgHeader.FindStringSubmatch(line); m != nil {
			kind = map[string]string{"": "install", "UPGRADED": "upgrade", "DOWNGRADED": "downgrade", "REINSTALLED": "reinstall", "REMOVED": "remove"}[m[2]]
			continue
		}
		if m := reSizeLine.FindStringSubmatch(line); m != nil {
			switch {
			case m[3] == "more space":
				v, _ := strconv.ParseFloat(m[1], 64)
				p.Size = int64(v * unit[m[2]])
			case m[3] == "to be downloaded":
				v, _ := strconv.ParseFloat(m[1], 64)
				p.Download = int64(v * unit[m[2]])
			default:
				v, _ := strconv.ParseFloat(m[4], 64)
				p.Size = -int64(v * unit[m[5]])
			}
			continue
		}
		if kind == "" || !strings.HasPrefix(raw, "\t") {
			continue
		}
		// "name: old -> new", "name: version (required by x)", "name-version" (reinstalled), each maybe with " [repo]"
		item := line
		if i := strings.Index(item, " ["); i > 0 {
			item = item[:i]
		}
		if i := strings.Index(item, " ("); i > 0 {
			item = item[:i]
		}
		c := pkgChange{Kind: kind}
		if n, v, ok := strings.Cut(item, ": "); ok {
			c.Name = n
			if a, b, ok := strings.Cut(v, " -> "); ok {
				c.From, c.To = a, b
			} else if kind == "remove" {
				c.From = v
			} else {
				c.To = v
			}
		} else if i := strings.LastIndex(item, "-"); i > 0 {
			c.Name, c.From, c.To = item[:i], item[i+1:], item[i+1:]
		} else {
			return pkgPlan{}, fmt.Errorf("pkg's dry run has a line that is not understood: %q", line)
		}
		p.Changes = append(p.Changes, c)
	}
	if len(p.Changes) == 0 && !nothing {
		return pkgPlan{}, fmt.Errorf("pkg's dry run is not understood: %s", tailLine(out))
	}
	sort.Slice(p.Changes, func(i, j int) bool { return p.Changes[i].Name < p.Changes[j].Name })
	return p, nil
}

// planThickUpgrade builds the in-place upgrade of a FreeBSD jail: an independent system whose base packages are
// upgraded from the host with pkg (`pkg -r ROOT`, the repository configuration outside the root) and whose ports
// packages are upgraded through the running jail (req.Ports, decided and run by the service). The jail is never
// stopped, started or restarted: the snapshot is taken while it runs, before the first package change, and is kept for a
// manual rollback; a failure anywhere, the health check included, is reported and leaves the root as pkg left it. The
// release of the jail is the one installed in it: the repository is that release's (base_release_<minor>, or
// base_latest for a branch), unless req.Release names a newer one.
//
//	base newer:   [prefetch], snapshot, repo conf, pkg update, pkg upgrade|install, verify, clean, settle, verify sums,
//	              [ports], restart pending, health check
//	only ports:   snapshot, ports, restart pending, health check
//	nothing:      no plan, nothing is touched
//
// The check for something newer is read-only and made before anything is snapshotted: the catalogue is read
// into a scratch root of the container, the jail's package database is copied into it, and pkg's own dry run on that
// copy says what changes (nothing is guessed). After pkg ran, every package must be at the version the dry run planned.
// No package script runs from the host; what the scripts of the changed packages do runs inside the jail (settle).
func (s *Service) planThickUpgrade(ctx context.Context, st *jailState, rec Record, req UpgradeRequest, res *UpgradeResult) (*plan.Plan, *UpgradeResult, error) {
	name := st.Name
	if rec.Spec.OS == api.OSLinux {
		return nil, res, api.Errorf(api.CodeUnsupported, "Linux jails look after their own packages: JailOps does not update them").WithHint("run apk or apt in the jail, for instance from its cron")
	}
	for _, v := range rec.Spec.Volumes {
		if SystemDir(v.Mount) {
			// the package database or the packages would be outside the root and its snapshot: a rollback would bring
			// the base back and leave the database as the upgrade wrote it
			return nil, res, api.Errorf(api.CodePreconditionFailed, "volume %q is mounted at %s, a directory of the jail's system: what pkg writes there is outside the root and its snapshot", v.Name, v.Mount).
				WithHint("move what the volume holds into a volume mounted below it (for example " + strings.TrimSuffix(v.Mount, "/") + "/data), detach this one, then upgrade")
		}
	}
	add := len(req.AddPackages) > 0
	if add {
		if err := checkBasePackages(req.AddPackages); err != nil {
			return nil, res, err
		}
		if req.Release != "" {
			return nil, res, api.Errorf(api.CodeInvalid, "adding base packages cannot be combined with a release")
		}
	}
	resume := req.Resume
	paths := s.Cfg.at(name)
	res.Kept = []string{"root"}

	running := false
	var err error
	if resume != nil {
		running = resume.WasRunning
	} else if running, err = s.Jails.Running(ctx, name); err != nil {
		return nil, res, err
	}
	stamp := s.stamp(req.Stamp)
	snapName := upgradeSnapPrefix + stamp
	res.Snapshot, res.Stamp = snapName, stamp
	res.State = SwitchState{WasRunning: running}

	// what is installed, and the release it is at
	var installed map[string]pkgInfo
	var inst thickRelease
	baseChange, portsStep := false, false
	var fetch []string
	// want is pkg's own plan: every package it changes and the version it gets ("" for one it removes)
	want := map[string]string{}
	targetRel := ""
	var abi, osver, repoURL, keys, repoBody string
	cacheDir := path.Join(paths.st.MountBase, "cache", "pkg")
	work := paths.containerMP() + "/.upgrade-" + stamp
	reposDir := work + "/repos"
	repoFile := reposDir + "/" + imagegen.PkgBaseRepoName + ".conf"

	if resume != nil {
		baseChange, portsStep = resume.BaseChange, resume.PortsStep
	} else {
		if err := s.refuseLocked(ctx, st); err != nil {
			return nil, res, err
		}
		// the checks read copies in a scratch root of the container: the jail's own root is not given to pkg
		// a name of its own: a check on request and an upgrade planned in the same second do not share it
		checkDir := fmt.Sprintf("%s-check%d", work, checkSeq.Add(1))
		defer func() { _ = s.Files.RemoveAll(context.WithoutCancel(ctx), checkDir) }()
		var script []byte
		var arch string
		if installed, script, arch, err = s.installedState(ctx, st, running, checkDir); err != nil {
			return nil, res, err
		}
		if inst, err = s.installedRelease(ctx, name, script, installed["FreeBSD-runtime"].Version); err != nil {
			return nil, res, err
		}
		// the release JailOps recorded (at create, upgrade and rollback, from a root no jail ran in) decides, not what
		// the root says now: /bin/freebsd-version and the package database are the tenant's to change
		if rec := s.prop(ctx, st.Root, PropFreeBSDVersion); rec != "" {
			known, kerr := s.installedRelease(ctx, name, []byte(`USERLAND_VERSION="`+rec+`"`), "")
			if kerr == nil {
				if known.Rel != inst.Rel && strings.TrimSpace(req.Release) == "" {
					return nil, res, api.Errorf(api.CodePreconditionFailed, "the root of jail %s says it is %s and JailOps installed %s: the release is not taken from the jail", plainName(name), inst.Full, known.Full).
						WithHint("name the release to upgrade to with --release, or roll the jail back")
				}
				inst = known
			}
		}
		res.Installed, res.FromRelease = inst.Full, inst.Rel
		res.From = inst.Full
		targetRel = inst.Rel
		switching := false
		host, err := s.Jails.HostRelease(ctx)
		if err != nil {
			return nil, res, err
		}
		if r := strings.TrimSpace(req.Release); r != "" {
			if !reReleaseArg.MatchString(r) {
				return nil, res, api.Errorf(api.CodeInvalid, "release %q is not a FreeBSD release: write 15.2-RELEASE, 15.2-STABLE, 16.0-CURRENT, 15.2-PRERELEASE, 15.2-BETA1 or 15.2-RC1", r)
			}
			older, err := releaseNewer(inst.Rel, r)
			if err != nil {
				return nil, res, api.Errorf(api.CodeInternal, "compare releases: %v", err)
			}
			if older {
				return nil, res, api.Errorf(api.CodeInvalid, "release %s is older than the %s installed in jail %s: a jail does not move to an older release", r, inst.Full, plainName(name)).
					WithHint("create a new jail from an older image instead")
			}
			if newer, err := releaseNewer(r, host); err != nil {
				return nil, res, api.Errorf(api.CodeInternal, "compare releases: %v", err)
			} else if newer {
				return nil, res, api.Errorf(api.CodeVersionSkew, "release %s is newer than the host kernel %s: a jail's userland is not newer than the kernel it runs on", r, host).
					WithHint("upgrade the host first")
			}
			a1, b1, _ := parseRel(inst.Rel)
			a2, b2, _ := parseRel(r)
			// RELEASE reads base_release_<minor>, every other type base_latest: PRERELEASE, STABLE and CURRENT of one
			// minor are one repository
			switching = a1 != a2 || b1 != b2 || strings.HasSuffix(r, "-RELEASE") != strings.HasSuffix(inst.Rel, "-RELEASE")
			targetRel = r
		}
		res.ToRelease = targetRel
		spec := api.ImageSpec{OS: api.OSFreeBSD, Arch: arch, Release: targetRel, Source: api.ImageSource{Type: "pkgbase"}}
		if abi, err = imagegen.PkgBaseABI(spec); err != nil {
			return nil, res, err
		}
		tm, _, _ := parseRel(targetRel)
		osver = s.hostOSVersion(ctx, host, tm)
		if repoURL, keys, repoBody, err = imagegen.PkgBaseRepoConf(spec); err != nil {
			return nil, res, err
		}
		// the catalogue of the release, read without touching the jail
		cat, err := s.catalogue(ctx, checkDir, abi, osver, repoURL, repoBody, cacheDir)
		if err != nil {
			return nil, res, err
		}
		if running {
			// the helper's detection (a root prepared against host-side pkg: a link out of the root on a path pkg touches,
			// a link among pkg's trees, a pkg configuration with plugins), once, before anything changes; a finding
			// refuses this jail with what was found (SuspectedError), and nothing is touched. It is made again on every
			// run: a jail that was cleaned goes on.
			if err := s.checkRoot(ctx, st, checkDir, installed); err != nil {
				return nil, res, err
			}
			// a clean preflight ends an earlier finding
			if s.prop(ctx, st.Container, PropSuspected) != "" {
				_ = s.ZFS.Inherit(ctx, st.Container, PropSuspected)
			}
		}
		addNothing := false
		if add {
			addNothing = true
			for _, n := range req.AddPackages {
				if _, ok := cat[n]; !ok {
					return nil, res, api.Errorf(api.CodeNotFound, "base package %q is not in the repository of FreeBSD %s", n, targetRel)
				}
				_, have := installed[n]
				addNothing = addNothing && have
			}
		}
		// pkg decides what changes, on the copy of the jail's package database: nothing is guessed
		dry := []string{"upgrade", "-n", "-U", "-r", imagegen.PkgBaseRepoName}
		switch {
		case add:
			dry = append([]string{"install", "-n", "-U", "-r", imagegen.PkgBaseRepoName}, req.AddPackages...)
		case switching:
			// the packages of another release are not newer by version alone
			dry = []string{"upgrade", "-f", "-n", "-U", "-r", imagegen.PkgBaseRepoName}
		}
		var pp pkgPlan
		if !addNothing {
			// every package asked for is installed already: nothing to add, and pkg is not asked
			out, perr := s.Exec.Run(ctx, thickPkgArgv(checkDir+"/scratch", checkDir+"/repos", cacheDir, abi, osver, dry...))
			if pp, err = parsePkgPlan(string(out)); err != nil {
				if perr != nil {
					return nil, res, &causedError{api.Errorf(api.CodePreconditionFailed, "pkg's dry run of the upgrade failed: %s", tailLine(imagegen.CleanOutput(string(out)))), perr}
				}
				return nil, res, api.Errorf(api.CodePreconditionFailed, "pkg's dry run said nothing JailOps understands, so nothing is changed: %s", tailLine(imagegen.CleanOutput(string(out))))
			}
		}
		for _, c := range pp.Changes {
			want[c.Name] = c.To
			res.BaseChanges = append(res.BaseChanges, c.String())
			if c.Kind != "remove" {
				fetch = append(fetch, c.Name)
			}
		}
		baseChange = len(pp.Changes) > 0
		res.Download, res.DiskDelta = pp.Download, pp.Size
		failCheck := func(err error) error {
			if req.DryRun {
				res.Failed = append(res.Failed, err.Error())
				return nil
			}
			return err
		}
		res.BasePending = fetch
		res.Checks = append(res.Checks, "the jail is a FreeBSD jail", "the repository of "+targetRel+" is reachable and its catalogue verified")
		if baseChange {
			res.Checks = append(res.Checks, fmt.Sprintf("pkg's dry run on a copy of the jail's package database: %d base packages change", len(pp.Changes)))
		}
		if req.Release != "" {
			res.Checks = append(res.Checks, "release "+targetRel+" is not older than "+inst.Rel+" and not newer than the host kernel")
		}
		if !req.NoPackages && req.Ports != nil && req.Ports.None && !add {
			res.Checks = append(res.Checks, "no ports packages: the ports catalogue is not fetched and there is no ports step")
		}
		if !req.NoPackages && req.Ports != nil && len(req.Ports.Pending) > 0 && !add {
			portsStep = true
			res.PortsPending = req.Ports.Pending
		}
		res.State.BaseChange, res.State.PortsStep = baseChange, portsStep
		if !baseChange && !portsStep {
			res.Nothing, res.Changed = true, false
			if !add {
				res.Checks = append(res.Checks, fmt.Sprintf("%d base packages are current in the repository of %s", len(installed), targetRel))
				if !req.NoPackages && req.Ports != nil && !req.Ports.None {
					res.Checks = append(res.Checks, "the ports packages are current")
				}
			}
			// a jail that pkg finds current has no upgrade left half-done: an old note of a failed rollback goes
			s.clearUpgradeIncomplete(ctx, st.Container)
			return nil, res, nil
		}
		// room for it, before anything is stopped: the root keeps the old files in the snapshot while the new ones are
		// written, so it needs about the size of the new packages; the downloads go to the cache of the storage
		var need int64
		for _, n := range fetch {
			need += cat[n].Size
		}
		if baseChange && need > 0 {
			rootNeed := need + need/10 + 16<<20
			if avail, ok := s.availableBytes(ctx, st.Root); ok && avail < rootNeed {
				if err := failCheck(api.Errorf(api.CodeQuotaExceeded, "jail %s has %s free in its root and the upgrade needs about %s (%d packages, and the snapshot keeps the old files)",
					plainName(name), humanBytes(avail), humanBytes(rootNeed), len(fetch)).
					WithHint("free space in the jail or raise its quota (jailops config set NAME resources.disk=...)")); err != nil {
					return nil, res, err
				}
			} else if ok {
				res.Checks = append(res.Checks, fmt.Sprintf("the root has room: %s free, about %s needed", humanBytes(avail), humanBytes(rootNeed)))
			}
			poolNeed := res.Download + 64<<20
			if res.Download == 0 {
				poolNeed = need/3 + 64<<20
			}
			if avail, ok := s.availableBytes(ctx, paths.st.Root); ok && avail < poolNeed {
				if err := failCheck(api.Errorf(api.CodePoolExhausted, "the storage has %s free and the downloads need about %s", humanBytes(avail), humanBytes(poolNeed)).
					WithHint("free space on the pool that holds the package cache")); err != nil {
					return nil, res, err
				}
			} else if ok {
				res.Checks = append(res.Checks, fmt.Sprintf("the storage has room for the downloads: %s free, about %s needed", humanBytes(avail), humanBytes(poolNeed)))
			}
		}
		// a jail that is unhealthy now is not a safe base to roll back to
		if hc := strings.Fields(rec.Spec.Update.Healthcheck); running && len(hc) > 0 {
			res.HealthCheck = strings.Join(hc, " ")
			if out, herr := s.Jails.Exec(ctx, name, hc); herr != nil && !req.Force {
				if err := failCheck(&causedError{api.Errorf(api.CodePreconditionFailed, "the health check of jail %s fails before the upgrade (%s: %v: %s): a rollback point that is already broken is not a safe base",
					plainName(name), res.HealthCheck, herr, tailLine(string(out))).WithHint("repair the jail first, or upgrade anyway with --force"), herr}); err != nil {
					return nil, res, err
				}
			} else if herr != nil {
				res.Checks = append(res.Checks, "the health check fails before the upgrade ("+res.HealthCheck+"): --force goes on anyway")
			} else {
				res.Checks = append(res.Checks, "the health check passes before the upgrade ("+res.HealthCheck+")")
			}
		}
		// never: the base of a jail is upgraded while it runs
		res.WillStop = false
		if !running && !req.DryRun {
			return nil, res, api.Errorf(api.CodePreconditionFailed, "jail %s does not run: JailOps upgrades a jail while it runs, and never starts or stops it for that", plainName(name)).
				WithHint("start the jail (jailops start " + plainName(name) + "), then upgrade it")
		}
		res.Changed = true
		res.To = targetRel
		switch {
		case add:
			res.To = res.From
		case !switching && baseChange && portsStep:
			res.To = "newer base and ports packages"
		case !switching && baseChange:
			res.To = "newer base packages"
		case !baseChange:
			res.To = "newer ports packages"
		}
		if s.Cfg.Replicating != nil {
			if busy, err := s.Cfg.Replicating(ctx, name); err != nil {
				return nil, res, err
			} else if busy {
				return nil, res, api.Errorf(api.CodeBusy, "replication of jail %s is running", name)
			}
		}
	}
	all := st.all()
	snaps := make([]string, len(all))
	for i, d := range all {
		snaps[i] = d + "@" + snapName
	}
	z := s.ZFS
	scratch := work + "/scratch"
	// host pkg -r on the root of the running jail, as the Handbook upgrades a pkgbase jail; the jail is never stopped
	liveArgv := func(args ...string) []string {
		return thickPkgArgv(paths.rootMP(), reposDir, cacheDir, abi, osver, args...)
	}
	runPkg := func(args ...string) func(context.Context) error {
		return func(ctx context.Context) error { return imagegen.RunPkg(ctx, s.Exec.Run, liveArgv(args...)) }
	}
	describe := func(args ...string) string { return strings.Join(liveArgv(args...), " ") }
	due := s.Cfg.now().Add(s.Cfg.RootOldRetention).UTC().Format(time.RFC3339)
	comment := "before the in-place upgrade"
	if add {
		comment = "before adding the base packages " + strings.Join(req.AddPackages, " ")
	}
	// the snapshot is taken while the jail runs (crash-consistent), for a person's recovery only: nothing rolls the
	// root back on its own, because a rollback under running services loses what they wrote since
	snapStep := plan.Step{Name: "snapshot", Describe: "zfs snapshot " + strings.Join(snaps, " ") + "  (while the jail runs, before the first package change; kept for a manual rollback: jailops jail rollback " + plainName(name) + ")",
		Do: func(ctx context.Context) error {
			props := map[string]string{PropCleanupAfter: due, PropComment: comment}
			if res.Installed != "" {
				// the release the root is at in the snapshot: a rollback to it records it again
				props[PropFreeBSDVersion] = res.Installed
			}
			return z.Snapshot(ctx, props, snaps...)
		}}

	pl := plan.New("upgrade-jail " + name)
	if baseChange {
		pl.Add(plan.Step{Name: "prefetch", Describe: fmt.Sprintf("pkg -r %s fetch -y -d -r %s  %d packages: %s  (the jail is not touched)", scratch, imagegen.PkgBaseRepoName, len(fetch), shortNames(fetch, 6)),
			Do: func(ctx context.Context) error {
				if len(fetch) == 0 {
					return nil
				}
				if _, err := s.catalogue(ctx, work, abi, osver, repoURL, repoBody, cacheDir); err != nil {
					return err
				}
				// the helper takes at most 64 arguments of one pkg command
				for rest := fetch; len(rest) > 0; {
					n := min(len(rest), 40)
					args := append([]string{"fetch", "-y", "-d", "-r", imagegen.PkgBaseRepoName}, rest[:n]...)
					if err := imagegen.RunPkg(ctx, s.Exec.Run, thickPkgArgv(scratch, reposDir, cacheDir, abi, osver, args...)); err != nil {
						return err
					}
					rest = rest[n:]
				}
				return nil
			},
			Undo: func(ctx context.Context) error { return s.Files.RemoveAll(ctx, work) }, UndoOnFail: true})
		pl.Add(snapStep)
		pl.Add(plan.Step{Name: "write-repo-conf", Describe: "write " + repoFile + " (url " + repoURL + ", fingerprints " + keys + "), outside the jail's root",
			Do: func(ctx context.Context) error {
				for _, d := range []string{reposDir, cacheDir} {
					if err := s.Files.MkdirAll(ctx, d); err != nil {
						return err
					}
				}
				return s.Files.WriteAtomic(ctx, repoFile, []byte(repoBody))
			},
			Undo: func(ctx context.Context) error { return s.Files.RemoveAll(ctx, work) }, UndoOnFail: true})
		pl.Add(plan.Step{Name: "pkg-update", Describe: describe("update", "-f", "-r", imagegen.PkgBaseRepoName),
			Do: runPkg("update", "-f", "-r", imagegen.PkgBaseRepoName)})
		pkgArgs, pkgName := []string{"upgrade", "-y", "-r", imagegen.PkgBaseRepoName}, "pkg-upgrade"
		switch {
		case add:
			pkgArgs, pkgName = append([]string{"install", "-y", "-r", imagegen.PkgBaseRepoName}, req.AddPackages...), "pkg-install"
		case res.ToRelease != "" && res.ToRelease != res.FromRelease:
			// the packages of another release are not newer by version alone
			pkgArgs = []string{"upgrade", "-f", "-y", "-r", imagegen.PkgBaseRepoName}
		}
		// no undo: a failure is reported and the jail runs as pkg left it; running pkg again completes it, and the
		// snapshot is there for a person
		pl.Add(plan.Step{Name: pkgName, Describe: describe(pkgArgs...) + "  (the jail runs; no package script runs from the host)", Do: runPkg(pkgArgs...)})
		major, minor, _ := parseRel(res.ToRelease)
		pl.Add(plan.Step{Name: "verify-release", Describe: describe("query", "%n %v") + ": every package is at the version pkg's dry run planned and the base is that of " + res.ToRelease,
			Do: func(ctx context.Context) error {
				if resume != nil {
					return nil
				}
				out, err := s.Exec.Run(ctx, liveArgv("query", "%n %v"))
				if err != nil {
					return fmt.Errorf("pkg query: %w: %s", err, strings.TrimSpace(string(out)))
				}
				now := parsePackages(string(out))
				if strings.Contains(strings.ToUpper(res.ToRelease), "-RELEASE") {
					if err := imagegen.VerifyBase(now["FreeBSD-runtime"].Version, major, minor, res.ToRelease); err != nil {
						return err
					}
				}
				if err := checkPlanned(now, installed, want); err != nil {
					return err
				}
				// the release recorded is what JailOps installed (pkg's plan), not what the running jail's files say
				res.InstalledAfter = res.Installed
				if v, ok := want["FreeBSD-runtime"]; ok {
					if r, err := s.installedRelease(ctx, name, nil, v); err == nil && strings.HasSuffix(r.Rel, "-RELEASE") {
						res.InstalledAfter = r.Full
					} else {
						res.InstalledAfter = res.ToRelease
					}
				}
				return nil
			}})
		pl.Add(plan.Step{Name: "verify-deps", Describe: describe("check", "-d", "-a") + "  (every dependency of every package is installed)",
			Do: func(ctx context.Context) error {
				if resume != nil {
					return nil
				}
				if out, err := s.Exec.Run(ctx, liveArgv("check", "-d", "-a")); err != nil {
					return &causedError{api.Errorf(api.CodePreconditionFailed, "the package dependencies of the upgraded root are not complete: %s", tailLine(string(out))), err}
				}
				return nil
			}})
		pl.Add(plan.Step{Name: "clean-work", Describe: "remove " + work,
			Do: func(ctx context.Context) error { return s.Files.RemoveAll(ctx, work) }})
		pl.Add(plan.Step{Name: "settle", Describe: "inside the jail, what the scripts of the base packages do (RUN_SCRIPTS=false: no script runs from the host): " + settleDescription(),
			Do: func(ctx context.Context) error {
				if resume != nil {
					return nil
				}
				for _, c := range settleOrder {
					if out, err := s.Jails.Exec(ctx, name, settleArgv[c]); err != nil {
						return &causedError{api.Errorf(api.CodePreconditionFailed, "%s inside jail %s failed: %s", strings.Join(settleArgv[c], " "), plainName(name), tailLine(string(out))), err}
					}
				}
				return nil
			}})
		pl.Add(plan.Step{Name: "verify-sums", Describe: "pkg -j " + name + " check -s " + shortNames(fetch, 4) + "  (the checksums of the changed packages' files, inside the jail), and a count of the *.pkgsave files",
			Do: func(ctx context.Context) error {
				if resume != nil {
					return nil
				}
				for rest := fetch; len(rest) > 0; {
					n := min(len(rest), 40)
					if out, err := s.Exec.Run(ctx, append([]string{"pkg", "-j", name, "check", "-s"}, rest[:n]...)); err != nil {
						return &causedError{api.Errorf(api.CodePreconditionFailed, "the checksums of the upgraded packages do not match: %s", tailLine(imagegen.CleanOutput(string(out)))), err}
					}
					rest = rest[n:]
				}
				// information, not an error: the configuration files pkg kept aside
				if out, err := s.Jails.Exec(ctx, name, []string{"find", "/etc", "/usr/local/etc", "-name", "*.pkgsave"}); err == nil {
					for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
						if l = strings.TrimSpace(l); l != "" {
							res.Pkgsave = append(res.Pkgsave, l)
						}
					}
				}
				return nil
			}})
	} else {
		pl.Add(snapStep)
	}
	if portsStep {
		portsRun := func(context.Context) error { return nil }
		if req.Ports != nil && req.Ports.Run != nil {
			portsRun = req.Ports.Run
		}
		// no undo either: a failure of the ports packages is reported and left, as a package update of the jail is
		pl.Add(plan.Step{Name: "ports", Describe: "pkg upgrade of the ports packages inside the jail (pkg -j " + name + "; base packages excluded by name): " + strings.Join(res.PortsPending, " "),
			Do: portsRun})
	}
	pl.Add(plan.Step{Name: "restart-pending", Describe: "list the processes of the jail that still run replaced files (procstat -v inside the jail); nothing is restarted",
		Do: func(ctx context.Context) error {
			if resume == nil {
				res.RestartPending = s.RestartPending(ctx, name)
			}
			return nil
		}})
	pl.Add(s.healthStep(name, rec, "on the upgraded root"))
	return pl, res, nil
}

// checkPlanned compares the base packages of the upgraded root (now) with pkg's plan (want: the version each changed
// package gets, "" for one pkg removes) and with what was installed before: a planned package that is not at its
// version, and a base package that changed without being planned, are errors. pkg is the judge before the run and
// after it; the result is never taken from the plan alone.
func checkPlanned(now, before map[string]pkgInfo, want map[string]string) error {
	var bad []string
	for n, v := range want {
		got, ok := now[n]
		switch {
		case v == "" && ok:
			bad = append(bad, n+" is still installed ("+got.Version+"), pkg planned to remove it")
		case v != "" && got.Version != v:
			bad = append(bad, fmt.Sprintf("%s is at %q, pkg planned %s", n, got.Version, v))
		}
	}
	for n, v := range before {
		if _, planned := want[n]; planned || !strings.HasPrefix(n, "FreeBSD-") {
			continue
		}
		if got, ok := now[n]; !ok || got.Version != v.Version {
			bad = append(bad, fmt.Sprintf("%s changed from %s to %q without being in pkg's plan", n, v.Version, got.Version))
		}
	}
	if len(bad) == 0 {
		return nil
	}
	sort.Strings(bad)
	return api.Errorf(api.CodePreconditionFailed, "the upgraded root is not what pkg planned: %s", shortNames(bad, 4))
}

// settleArgv are what the scripts of the base packages do (FreeBSD 15 and 16: FreeBSD-runtime rebuilds the password
// and the services databases, FreeBSD-utilities the login class database, FreeBSD-caroot the links of the trusted
// certificates; no other base package of the minimal sets has a script), as the commands that do it inside the jail:
// pkg runs with RUN_SCRIPTS=false, because a script run from the host with PKG_ROOTDIR follows links of the jail. They
// are idempotent and run after every base change. A script a later release adds is not run (security-model.md).
// settleOrder is the order they run in.
var (
	settleArgv = map[string][]string{
		"pwd_mkdb":      {"pwd_mkdb", "-i", "-p", "-d", "/etc", "/etc/master.passwd"},
		"services_mkdb": {"services_mkdb", "-l", "-q", "-o", "/var/db/services.db", "/etc/services"},
		"cap_mkdb":      {"cap_mkdb", "-l", "/etc/login.conf"},
		"certctl":       {"certctl", "rehash"},
	}
	settleOrder = []string{"pwd_mkdb", "services_mkdb", "cap_mkdb", "certctl"}
)

func settleDescription() string {
	var parts []string
	for _, c := range settleOrder {
		parts = append(parts, strings.Join(settleArgv[c], " "))
	}
	return strings.Join(parts, "; ")
}

// cleanupUpgradeSnapshots destroys the pre-upgrade snapshots of a jail whose retention has passed. It reports
// whether it destroyed any. A snapshot without the cleanup mark that an upgrade sets is never touched.
func (s *Service) cleanupUpgradeSnapshots(ctx context.Context, st *jailState) (bool, error) {
	names, err := s.ZFS.Snapshots(ctx, st.Container)
	if err != nil {
		return false, err
	}
	destroyed := false
	for _, n := range names {
		if !strings.HasPrefix(n, upgradeSnapPrefix) {
			continue
		}
		mark := s.prop(ctx, st.Container+"@"+n, PropCleanupAfter)
		due, err := time.Parse(time.RFC3339, mark)
		if err != nil || s.Cfg.now().Before(due) {
			continue
		}
		for _, d := range st.all() {
			if ok, _ := s.ZFS.Exists(ctx, d+"@"+n); !ok {
				continue
			}
			if err := s.ZFS.Destroy(ctx, d+"@"+n); err != nil {
				return destroyed, err
			}
			destroyed = true
		}
	}
	return destroyed, nil
}

// checkBasePackages accepts the names of base packages to add: FreeBSD-* names, no option, no path, no version.
func checkBasePackages(names []string) error {
	if len(names) > 32 {
		return api.Errorf(api.CodeInvalid, "at most 32 base packages are added at once")
	}
	for _, n := range names {
		if !reBasePackage.MatchString(n) {
			return api.Errorf(api.CodeInvalid, "%q is not a base package name: base packages are named FreeBSD-<name>, for example FreeBSD-ssh", n)
		}
	}
	return nil
}

var reBasePackage = regexp.MustCompile(`^FreeBSD-[A-Za-z0-9][A-Za-z0-9._+-]{0,100}$`)

// tailLine is the last non-empty line of a message.
func tailLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// causedError is an api.Error that keeps the error it was made from in the chain, for errors.Is.
type causedError struct {
	ae    *api.Error
	cause error
}

func (e *causedError) Error() string   { return e.ae.Error() }
func (e *causedError) Unwrap() []error { return []error{e.ae, e.cause} }

func shortNames(n []string, max int) string {
	if len(n) <= max {
		return strings.Join(n, " ")
	}
	return strings.Join(n[:max], " ") + fmt.Sprintf(" ... (%d more)", len(n)-max)
}

// availableBytes is the space a dataset can still take (its own quota and its parents' included), when the pool says.
func (s *Service) availableBytes(ctx context.Context, ds string) (int64, bool) {
	n, err := strconv.ParseInt(strings.TrimSpace(s.prop(ctx, ds, "available")), 10, 64)
	return n, err == nil && n > 0
}

// humanBytes writes a size as KiB, MiB or GiB.
func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// PropFreeBSDVersion holds, on the root of a FreeBSD jail, the release installed in it (freebsd-version -u), set
// when the jail is made, upgraded or rolled back: what `jail show` reports, also for a stopped jail whose root is not
// mounted.
const PropFreeBSDVersion = "jailops:freebsd_version"

// recordInstalledRelease notes the release installed in a jail's root (PropFreeBSDVersion). The root is mounted
// for the read when the jail does not run. A root that does not say leaves the property as it was.
func (s *Service) recordInstalledRelease(ctx context.Context, name string) {
	st, err := s.observe(ctx, name)
	if err != nil || st.Unsupported {
		return
	}
	if up, _ := s.Jails.Running(ctx, name); !up {
		if err := s.mountTree(ctx, name); err != nil {
			return
		}
		defer func() { _ = s.unmountTree(context.WithoutCancel(ctx), name) }()
	}
	b, err := s.Files.ReadFile(ctx, path.Join(s.Cfg.at(name).rootMP(), "bin/freebsd-version"))
	if err != nil {
		return
	}
	if m := reUserland.FindSubmatch(b); m != nil {
		_ = s.ZFS.Set(context.WithoutCancel(ctx), st.Root, map[string]string{PropFreeBSDVersion: string(m[1])})
	}
}

// RestartPending lists the processes of a running jail that still map files an update replaced: procstat -v inside the
// jail shows such a mapping as a vnode without a path (VERIFIED on FreeBSD 16: the services kept their old libc and
// binary after a live upgrade). It is information: nothing is restarted. A jail that cannot answer gives nothing.
func (s *Service) RestartPending(ctx context.Context, name string) []string {
	// procstat answers non-zero when a process ended while it looked: what it printed is still used
	out, _ := s.Jails.Exec(ctx, name, []string{"procstat", "-a", "-v"})
	pids := map[string]bool{}
	for _, l := range strings.Split(string(out), "\n") {
		f := strings.Fields(l)
		// PID START END PRT RES PRES REF SHD FLAG TP [PATH]: a vnode mapping (TP vn) with no path
		if len(f) == 10 && f[9] == "vn" {
			pids[f[0]] = true
		}
	}
	if len(pids) == 0 {
		return nil
	}
	ps, _ := s.Jails.Exec(ctx, name, []string{"ps", "-ax", "-o", "pid=", "-o", "comm="})
	var list []string
	for _, l := range strings.Split(string(ps), "\n") {
		f := strings.Fields(l)
		if len(f) == 2 && pids[f[0]] && f[1] != "procstat" && f[1] != "ps" {
			list = append(list, f[1]+" (pid "+f[0]+")")
		}
	}
	sort.Strings(list)
	return list
}

// PropSuspected marks a jail whose root the preflight detection of an upgrade found prepared against host-side pkg (a
// link out of the root, a link among pkg's trees, a pkg configuration with plugins): what was found. A later clean
// preflight clears it.
const PropSuspected = "jailops:suspected_compromised"

// NoteSuspected records the finding of the preflight detection on the jail's container.
func (s *Service) NoteSuspected(ctx context.Context, name, found string) {
	if len(found) > 1000 {
		found = found[:1000]
	}
	_ = s.ZFS.Set(context.WithoutCancel(ctx), s.Cfg.at(name).containerDS(), map[string]string{PropSuspected: found})
}

// Suspected is the finding NoteSuspected recorded on a jail, empty when there is none.
func (s *Service) Suspected(ctx context.Context, name string) string {
	return s.prop(ctx, s.Cfg.at(name).containerDS(), PropSuspected)
}

// PropUpgradeIncomplete marks a jail whose upgrade failed and whose plan could not undo its own steps (the undo errors
// of the plan). It holds what failed; the next successful upgrade or rollback clears it.
const PropUpgradeIncomplete = "jailops:upgrade_incomplete"

// NoteUpgradeFailure records on the container of a jail that an upgrade failed and the undo of one of its steps did too
// (the plan error carries undo errors), so that the daemon can report it until someone repairs it. It returns the note,
// empty when there was no undo error.
func (s *Service) NoteUpgradeFailure(ctx context.Context, name string, err error) string {
	var pe *plan.Error
	if !errors.As(err, &pe) || len(pe.UndoErrs) == 0 {
		return ""
	}
	parts := make([]string, len(pe.UndoErrs))
	for i, u := range pe.UndoErrs {
		parts[i] = u.Error()
	}
	note := fmt.Sprintf("%s failed at step %s and its undo did not finish: %s", pe.Plan, pe.Step, strings.Join(parts, "; "))
	if len(note) > 1000 {
		note = note[:1000]
	}
	st, oerr := s.observe(ctx, name)
	if oerr != nil {
		return note
	}
	_ = s.ZFS.Set(context.WithoutCancel(ctx), st.Container, map[string]string{PropUpgradeIncomplete: note})
	return note
}

// UpgradeIncomplete is the note NoteUpgradeFailure left on a jail, empty when there is none.
func (s *Service) UpgradeIncomplete(ctx context.Context, name string) string {
	return s.prop(ctx, s.Cfg.at(name).containerDS(), PropUpgradeIncomplete)
}

// ClearUpgradeIncomplete removes the note of a failed rollback from a jail whose state is known again: the replay of the
// interrupted plan finished its undo.
func (s *Service) ClearUpgradeIncomplete(ctx context.Context, name string) {
	s.clearUpgradeIncomplete(ctx, s.Cfg.at(name).containerDS())
}

func (s *Service) clearUpgradeIncomplete(ctx context.Context, container string) {
	if s.prop(ctx, container, PropUpgradeIncomplete) != "" {
		_ = s.ZFS.Inherit(ctx, container, PropUpgradeIncomplete)
	}
}
