package imagegen

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"jailops.net/jailops/internal/plan"
	"jailops.net/jailops/internal/render"
	"jailops.net/jailops/sdk/api"
)

// Build is the context a Source gets when it plans a generation.
type Build struct {
	M      *Manager
	Image  string
	Spec   api.ImageSpec
	Number int
	Parent int    // 0 when built from the source
	Root   string // mountpoint of the staging dataset
	Work   string // scratch directory for this build
}

// Source knows how to put a base system into a staging root, how to upgrade
// an existing one and how to read the installed package set. Everything that
// depends on FreeBSD, Alpine or Debian tooling lives behind this interface and
// is UNVERIFIED until run on a FreeBSD host.
type Source interface {
	Type() string
	CheckSpec(api.ImageSpec) error
	// Install plans a fresh install into an empty root.
	Install(b *Build) ([]plan.Step, error)
	// Upgrade plans an upgrade of the parent generation's packages in the clone.
	Upgrade(b *Build) ([]plan.Step, error)
	// Check plans source specific validation after install or upgrade.
	Check(b *Build) []plan.Step
	// Packages reads the installed package set.
	Packages(ctx context.Context, b *Build) ([]Package, error)
	// RequiredPaths must exist in the root of a valid generation.
	RequiredPaths() []string
}

// DefaultSources returns the built-in sources.
func DefaultSources() map[string]Source {
	return map[string]Source{
		"pkgbase":     PkgBase{},
		"minirootfs":  Minirootfs{},
		"debootstrap": Debootstrap{},
	}
}

func quote(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		if a == "" || strings.ContainsAny(a, " \t\"'$\\*?&;|<>(){}[]") {
			parts[i] = strconv.Quote(a)
		} else {
			parts[i] = a
		}
	}
	return strings.Join(parts, " ")
}

// reTerm matches the escape sequences a program writes to a terminal (colours, erase line, cursor moves).
var reTerm = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]")

// CleanOutput makes the output of a program fit for an error or a record: escape sequences are removed and a line that
// was redrawn with carriage returns (a progress bar) keeps its last state.
func CleanOutput(s string) string {
	s = reTerm.ReplaceAllString(s, "")
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	for i, l := range lines {
		if j := strings.LastIndex(l, "\r"); j >= 0 {
			lines[i] = l[j+1:]
		}
	}
	return strings.Join(lines, "\n")
}

func tail(b []byte) string {
	s := strings.TrimSpace(CleanOutput(string(b)))
	if len(s) > 400 {
		s = "..." + s[len(s)-400:]
	}
	return s
}

func (m *Manager) execStep(name string, argv []string) plan.Step {
	return plan.Step{
		Name: name, Describe: quote(argv),
		Do: func(ctx context.Context) error {
			out, err := m.Exec.Run(ctx, argv)
			if err != nil {
				return fmt.Errorf("%s: %w: %s", quote(argv), err, tail(out))
			}
			return nil
		},
	}
}

func (m *Manager) cleanWork(work string) plan.Step {
	return plan.Step{
		Name: "clean-work", Describe: "remove " + work,
		Do:   func(ctx context.Context) error { return m.Files.RemoveAll(ctx, work) },
		Undo: func(ctx context.Context) error { return m.Files.RemoveAll(ctx, work) },
	}
}

func confValue(name, v string) error {
	if strings.ContainsAny(v, "\"\\\n\r\x00") {
		return api.Errorf(api.CodeInvalid, "%s contains a character that is not allowed in a repository configuration", name)
	}
	return nil
}

// ---- pkgbase (FreeBSD) ----

// PkgBase builds FreeBSD generations from pkgbase packages with pkg(8) -r.
// UNVERIFIED: every pkg invocation below, including `-r`, `-o ABI=`,
// `-o REPOS_DIR=` and the pkgbase repository layout, is FreeBSD knowledge that
// has not run here (design section 16, item 2).
type PkgBase struct{}

func (PkgBase) Type() string { return "pkgbase" }

func parseRelease(rel string) (major, minor int, err error) {
	v, _, _ := strings.Cut(rel, "-")
	a, b, ok := strings.Cut(v, ".")
	if !ok {
		return 0, 0, fmt.Errorf("release %q is not <major>.<minor>-<type>", rel)
	}
	if major, err = strconv.Atoi(a); err == nil {
		minor, err = strconv.Atoi(b)
	}
	return
}

func (PkgBase) abi(spec api.ImageSpec) (string, error) {
	major, _, err := parseRelease(spec.Release)
	if err != nil {
		return "", api.Errorf(api.CodeInvalid, "%v", err)
	}
	arch := map[string]string{"amd64": "amd64", "arm64": "aarch64"}[spec.Arch]
	if arch == "" {
		return "", api.Errorf(api.CodeInvalid, "unsupported architecture %q", spec.Arch)
	}
	return fmt.Sprintf("FreeBSD:%d:%s", major, arch), nil
}

func (p PkgBase) CheckSpec(s api.ImageSpec) error {
	if s.OS != api.OSFreeBSD {
		return api.Errorf(api.CodeInvalid, "pkgbase images are FreeBSD images")
	}
	if _, err := p.abi(s); err != nil {
		return err
	}
	for _, v := range append([]string{s.Source.Repo, s.Source.Keys}, s.Source.Sets...) {
		if err := confValue("source", v); err != nil {
			return err
		}
	}
	return nil
}

func (PkgBase) RequiredPaths() []string { return []string{"/bin/sh", "/libexec/ld-elf.so.1"} }

func (PkgBase) sets(s api.ImageSpec) []string {
	if len(s.Source.Sets) > 0 {
		return s.Source.Sets
	}
	// The smallest base of a working jail: the minimal jail set (and sysrc), as in the pkgbase jail recipe. The FreeBSD-base
	// repository stays disabled inside a jail, as FreeBSD ships it, so pkg there sees the ports only and no
	// FreeBSD-* package (clibs-lib32 for llvm, which cannot be installed in a jail, FreeBSD bug 293817) is ever pulled in.
	// The base is updated from the host. No sshd: a jail that is entered with ssh names FreeBSD-ssh, and anything else a
	// program needs is a base package away (jailops jail base add; the base of a jail is the jail's own). The apps of the
	// catalogue (PostgreSQL, Valkey) run on this set; an image whose software needs more of the base names the optional
	// jail set (about 105 packages, 160 MiB) in source.sets, and the development set (compiler and headers, 570 MiB) when
	// the image builds software. Two libraries are added because ports packages link them and the base repository is
	// disabled on a host that is not installed from packages (the ports repository cannot supply a FreeBSD-* package):
	// FreeBSD-libexecinfo (PostgreSQL, Valkey) and FreeBSD-kerberos-lib (curl, so git over https in Forgejo).
	// sysrc is the tool an administrator reaches for first (sysrc postgresql_enable=YES): it is in FreeBSD-rc on 16, which
	// the minimal jail set holds, and in FreeBSD-bsdconfig (1 MiB, it needs only the runtime) on 15, which it does not.
	sets := []string{"FreeBSD-set-minimal-jail", "FreeBSD-libexecinfo", "FreeBSD-kerberos-lib"}
	if major, _ := strconv.Atoi(strings.SplitN(s.Release, ".", 2)[0]); major > 0 && major < 16 {
		sets = append(sets, "FreeBSD-bsdconfig")
	}
	return sets
}

// pkgArgv builds a `pkg -r <root>` argv with a private repository
// directory. The ABI is the one the release names (abi). Verified on
// FreeBSD 15.1 (prototype 2): -o IGNORE_OSVERSION=yes and -o ABI=<abi> are
// needed because the host's pkg judges the root, not itself.
func (p PkgBase) pkgArgv(b *Build, abi string, args ...string) []string {
	if abi == "" {
		abi, _ = p.abi(b.Spec)
	}
	return PkgBaseArgv(b.Root, path.Join(b.Work, "repos"), b.M.Layout.PkgCache(), abi, args...)
}

// PkgBaseArgv is `pkg -r <root>` with a private repository directory and package cache: the form the helper's pkg
// rule accepts for a root no jail runs in. abi is the one the release names (PkgBaseABI).
func PkgBaseArgv(root, reposDir, cacheDir, abi string, args ...string) []string {
	base := []string{"pkg", "-r", root, "-o", "ABI=" + abi, "-o", "REPOS_DIR=" + reposDir, "-o", "IGNORE_OSVERSION=yes", "-o", "PKG_CACHEDIR=" + cacheDir}
	return append(base, args...)
}

// PkgBaseABI is the pkg ABI of a FreeBSD release and architecture: "FreeBSD:15:amd64".
func PkgBaseABI(spec api.ImageSpec) (string, error) { return PkgBase{}.abi(spec) }

// PkgBaseRepoName is the name of the repository block PkgBaseRepoConf writes.
const PkgBaseRepoName = "jailops"

// pkgRun is the handle the steps of one plan share: the ABI learned from
// the host.
type pkgRun struct {
	p   PkgBase
	b   *Build
	abi string
}

func (r *pkgRun) step(name string, args ...string) plan.Step {
	return plan.Step{
		Name: name, Describe: quote(r.p.pkgArgv(r.b, r.abi, args...)),
		Do: func(ctx context.Context) error {
			argv := r.p.pkgArgv(r.b, r.abi, args...)
			return RunPkg(ctx, r.b.M.Exec.Run, argv)
		},
	}
}

// RunPkg runs a pkg argv. A mirror that does not answer for a moment must not fail an image build or an upgrade: pkg
// keeps what it already fetched in its cache, so a second try only asks for what is missing. The error carries the
// tail of the output.
func RunPkg(ctx context.Context, run func(context.Context, []string) ([]byte, error), argv []string) error {
	for try := 0; ; try++ {
		out, err := run(ctx, argv)
		if err == nil {
			return nil
		}
		if try >= fetchRetries || !transientFetch(out) {
			return fmt.Errorf("%s: %w: %s", quote(argv), err, tail(out))
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s: %w: %s", quote(argv), err, tail(out))
		case <-time.After(fetchBackoff << try):
		}
	}
}

var (
	// fetchRetries is how many more times a pkg step is tried when a mirror could not be reached.
	fetchRetries = 3
	// fetchBackoff is the wait before the first retry; it doubles each time.
	fetchBackoff = 5 * time.Second
)

// transientFetch says whether the output of a failed pkg run is a network failure that a retry can cure: a mirror
// that cannot be reached or answers with a server error. A signature that does not match, a missing package or an
// unknown option are never retried.
func transientFetch(out []byte) bool {
	low := strings.ToLower(string(out))
	for _, m := range []string{
		"no route to host", "operation timed out", "connection timed out", "connection refused", "connection reset",
		"network is unreachable", "temporary failure in name resolution", "could not resolve", "service unavailable",
		"bad gateway", "gateway timeout", "http error 5", "502 ", "503 ", "504 ", "try again",
	} {
		if strings.Contains(low, m) {
			return true
		}
	}
	return false
}

// abiStep checks that the host's pkg answers. The ABI of the image is the one its release names: the host's own ABI
// is no part of an image (a host of one major release builds the images of another), so the answer is not used.
func (r *pkgRun) abiStep() plan.Step {
	return plan.Step{
		Name: "pkg-abi", Describe: "pkg config abi",
		Do: func(ctx context.Context) error {
			out, err := r.b.M.Exec.Run(ctx, []string{"pkg", "config", "abi"})
			if err != nil {
				return fmt.Errorf("pkg config abi: %w: %s", err, tail(out))
			}
			return nil
		},
	}
}

// verifyReleaseStep refuses an image of a RELEASE whose base packages are of another release: the repository of a
// release is addressed by its minor version, and a mistake there would otherwise build a quietly wrong generation.
func (r *pkgRun) verifyReleaseStep() plan.Step {
	spec := r.b.Spec
	return plan.Step{
		Name: "verify-release", Describe: `pkg query "%n %v" FreeBSD-runtime: the version is that of ` + spec.Release,
		Do: func(ctx context.Context) error {
			if !isRelease(spec.Release) {
				return nil
			}
			major, minor, err := parseRelease(spec.Release)
			if err != nil {
				return api.Errorf(api.CodeInvalid, "%v", err)
			}
			argv := r.p.pkgArgv(r.b, r.abi, "query", "%n %v", "FreeBSD-runtime")
			out, err := r.b.M.Exec.Run(ctx, argv)
			if err != nil {
				return fmt.Errorf("%s: %w: %s", quote(argv), err, tail(out))
			}
			version := ""
			for _, l := range strings.Split(string(out), "\n") {
				if n, v, ok := strings.Cut(strings.TrimSpace(l), " "); ok && n == "FreeBSD-runtime" {
					version = v
				}
			}
			return VerifyBase(version, major, minor, spec.Release)
		},
	}
}

// VerifyBase checks the version of the base packages ("15.1p7") against the release the image is built for.
func VerifyBase(version string, major, minor int, release string) error {
	want := fmt.Sprintf("%d.%d", major, minor)
	if rest, ok := strings.CutPrefix(version, want); ok && (rest == "" || rest[0] == 'p' || rest[0] == '_' || rest[0] == ',') {
		return nil
	}
	return api.Errorf(api.CodeInvalid, "the base packages are version %q, not those of %s: the image would not be what its name says", version, release)
}

// keysSteps copy the host's key directories into the root before the first
// `pkg -r`: an empty root cannot verify any repository until the keys are
// there (verified on FreeBSD 15.1).
func (p PkgBase) keysSteps(b *Build) []plan.Step {
	m := b.M
	return []plan.Step{
		{Name: "seed-keys", Describe: "copy the host's /usr/share/keys into " + path.Join(b.Root, "usr/share/keys"),
			Do: func(ctx context.Context) error { return m.Files.SeedKeys(ctx, b.Root) }},
	}
}

func isRelease(rel string) bool {
	return strings.Contains(strings.ToUpper(rel), "-RELEASE")
}

// repoConf writes the private repository file. ${ABI} and ${VERSION_MAJOR} are
// left for pkg to expand. A RELEASE reads base_release_<minor> with the minor
// of the image's release written out: pkg expands ${VERSION_MINOR} from the
// ABI it was given, which carries no minor, so it answered 0 and a 15.1 image
// was built from the 15.0 packages. Anything else (STABLE, CURRENT) reads
// base_latest. The key directory is chosen once the host's keys are in the root
// (PkgBaseKeys), so the step runs after seed-keys.
func (p PkgBase) repoConf(b *Build) plan.Step {
	m := b.M
	file := path.Join(b.Work, "repos", "jailops.conf")
	url, keys, _, err := PkgBaseRepoConf(b.Spec)
	if err != nil {
		return plan.Step{Name: "write-repo-conf", Describe: "write the repository file",
			Do: func(context.Context) error { return err }}
	}
	if b.Spec.Source.Keys == "" {
		keys = "those of the host's FreeBSD-base repository, else " + strings.Join(keyCandidates(url, b.Spec.Release, "", ""), " or ")
	}
	return plan.Step{
		Name: "write-repo-conf", Describe: "write " + file + " (url " + url + ", fingerprints " + keys + ")",
		// Found on FreeBSD: the helper's atomic write does not create parent directories.
		Do: func(ctx context.Context) error {
			keys, err := PkgBaseKeys(ctx, m.Files, b.Root, b.Spec, url)
			if err != nil {
				return err
			}
			if err := m.Files.MkdirAll(ctx, path.Dir(file)); err != nil {
				return err
			}
			return m.Files.WriteAtomic(ctx, file, []byte(pkgBaseRepoBody(url, keys)))
		},
		Undo: func(ctx context.Context) error { return m.Files.RemoveAll(ctx, b.Work) },
	}
}

// PkgBaseRepoConf is the repository file of a pkgbase image: its url, its fingerprints directory and the file body.
// The file is the only form the helper accepts (internal/priv/pkgrepo.go). Without --keys the key directory is the one
// of the repository's kind; an image build checks that the host ships it (PkgBaseKeys).
func PkgBaseRepoConf(spec api.ImageSpec) (url, keys, body string, err error) {
	url = spec.Source.Repo
	keys = spec.Source.Keys
	if url == "" {
		if isRelease(spec.Release) {
			_, minor, perr := parseRelease(spec.Release)
			if perr != nil {
				return "", "", "", api.Errorf(api.CodeInvalid, "%v", perr)
			}
			url = fmt.Sprintf("pkg+https://pkg.FreeBSD.org/${ABI}/base_release_%d", minor)
		} else {
			url = "pkg+https://pkg.FreeBSD.org/${ABI}/base_latest"
		}
	}
	if keys == "" {
		keys = keyCandidates(url, spec.Release, "", "")[0]
	}
	return url, keys, pkgBaseRepoBody(url, keys), nil
}

func pkgBaseRepoBody(url, keys string) string {
	mirror := ""
	if strings.HasPrefix(url, "pkg+") {
		mirror = "  mirror_type: \"srv\",\n"
	}
	return repoBlock(PkgBaseRepoName, url, mirror, keys)
}

func repoBlock(name, url, mirror, keys string) string {
	return fmt.Sprintf("%s: {\n  url: \"%s\",\n%s  signature_type: \"fingerprints\",\n  fingerprints: \"%s\",\n  enabled: yes\n}\n", name, url, mirror, keys)
}

func (p PkgBase) Install(b *Build) ([]plan.Step, error) {
	r := &pkgRun{p: p, b: b}
	steps := append(append([]plan.Step{p.cacheStep(b), r.abiStep()}, p.keysSteps(b)...), p.repoConf(b))
	steps = append(steps,
		r.step("pkg-update", "update", "-f", "-r", "jailops"),
		r.step("pkg-install", append([]string{"install", "-y", "-r", "jailops"}, p.sets(b.Spec)...)...),
		r.verifyReleaseStep(),
		r.step("pkg-clean", "clean", "-y"),
		p.homeStep(b), p.rcConfStep(b), p.periodicConfStep(b), p.pkgReposStep(b), p.baseRepoOffStep(b), p.lockRootStep(b))
	return steps, nil
}

// cacheStep creates the shared package cache dataset (Layout.CacheDS) when it is missing, and removes it again if
// this build made it and fails: a build leaves nothing behind, and the next one makes it anew.
func (PkgBase) cacheStep(b *Build) plan.Step {
	m := b.M
	ds := m.Layout.CacheDS()
	made := false
	return plan.Step{Name: "ensure-cache-dataset", Describe: "zfs create " + ds + " (the package cache, mounted at " + m.Layout.CacheMount() + ") if missing",
		Do: func(ctx context.Context) error {
			if ok, err := m.ZFS.Exists(ctx, ds); err != nil || ok {
				return err
			}
			made = true
			return m.ZFS.Create(ctx, ds, map[string]string{PropManaged: "on", PropKind: "cache", "mountpoint": m.Layout.CacheMount()})
		},
		Undo: func(ctx context.Context) error {
			if ok, err := m.ZFS.Exists(ctx, ds); !made || err != nil || !ok {
				return err
			}
			return m.ZFS.DestroyRecursive(ctx, ds)
		},
		UndoOnFail: true}
}

// homeStep makes /home: the minimal jail set ships no /home, and a jail wants it for the home directories of its users
// (adduser, the data an administrator keeps there) as a FreeBSD system has it. An existing directory is kept.
func (PkgBase) homeStep(b *Build) plan.Step {
	m, dir := b.M, path.Join(b.Root, "home")
	return plan.Step{Name: "ensure-home", Describe: "mkdir " + dir + " (the minimal jail set has no /home) unless it exists",
		Do: func(ctx context.Context) error { return m.Files.MkdirAll(ctx, dir) }}
}

// jailRcConf is the /etc/rc.conf a new generation starts with (the base ships none): what a jail wants of rc
// whatever its network is. sendmail would start an MTA in every jail, syslogd would open a network socket, and cron
// starts the jobs of the superuser (periodic among them) at a random second of the first minute, so that the hundreds of
// jails of a host do not all start theirs at once.
const jailRcConf = `# Defaults for a JailOps jail. Change them with sysrc(8); JailOps keeps its own settings in /etc/rc.conf.local.
sendmail_enable="NONE"
syslogd_flags="-ss"
cron_flags="-J 60"
`

// rcConfStep writes jailRcConf, unless the image already has an /etc/rc.conf.
func (PkgBase) rcConfStep(b *Build) plan.Step {
	m, dst := b.M, path.Join(b.Root, "etc/rc.conf")
	return plan.Step{Name: "write-rc-conf", Describe: "write " + dst + " (sendmail off, syslogd without a network socket) unless it exists",
		Do: func(ctx context.Context) error {
			if ok, err := m.Files.Exists(ctx, dst); err != nil || ok {
				return err
			}
			return m.Files.WriteAtomic(ctx, dst, []byte(jailRcConf))
		}}
}

// jailPeriodicConf is the /etc/periodic.conf a new generation starts with. A jail runs the kernel of the host, so the
// daily check that the running kernel and the installed userland agree is wrong in every jail whenever the host is on
// another patch level than the image; it would report that on every day.
//
// The reports of periodic go to files of /var/log, which newsyslog already rotates, and not by mail to root: a jail
// has no MTA, and the mail would wait in a queue that nobody reads.
const jailPeriodicConf = `# Defaults for a JailOps jail. Change them with sysrc(8) -f /etc/periodic.conf.
daily_status_world_kernel="NO"
daily_output="/var/log/daily.log"
weekly_output="/var/log/weekly.log"
monthly_output="/var/log/monthly.log"
daily_status_security_output="/var/log/security"
weekly_status_security_output="/var/log/security"
monthly_status_security_output="/var/log/security"
`

// periodicConfStep writes jailPeriodicConf, unless the image already has an /etc/periodic.conf.
func (PkgBase) periodicConfStep(b *Build) plan.Step {
	m, dst := b.M, path.Join(b.Root, "etc/periodic.conf")
	return plan.Step{Name: "write-periodic-conf", Describe: "write " + dst + " (no kernel and userland comparison in daily status: a jail has the kernel of the host) unless it exists",
		Do: func(ctx context.Context) error {
			if ok, err := m.Files.Exists(ctx, dst); err != nil || ok {
				return err
			}
			return m.Files.WriteAtomic(ctx, dst, []byte(jailPeriodicConf))
		}}
}

// lockRootStep locks the password of root in the image: the base ships root with an empty one. Root of a jail is
// reached with jexec(8), which asks for no password, and a service that logs in (sshd, login) must not take an empty one.
func (PkgBase) lockRootStep(b *Build) plan.Step {
	return b.M.execStep("lock-root-password", []string{"pw-lock-root", "-R", b.Root, "usermod", "root", "-w", "no"})
}

// jailPkgRepos is /usr/local/etc/pkg/repos/FreeBSD.conf of a new generation: the kernel modules repository is of no use
// in a jail (the kernel is the host's) and costs a catalogue fetch in every pkg update.
const jailPkgRepos = `# Defaults for a JailOps jail: the kernel is the host's, so no kernel modules from the ports.
FreeBSD-ports-kmods: { enabled: no }
`

// BaseRepoOffPath and BaseRepoOff are the file that keeps the FreeBSD-base repository disabled inside a jail, as FreeBSD
// 15 ships it (contract rule 2): FreeBSD 16 ships it enabled in /etc/pkg/FreeBSD.conf (verified on 16.0-CURRENT), and a
// file of /usr/local/etc/pkg/repos overrides that. The base of a jail is upgraded from the host; inside the jail
// pkg handles the ports packages only. Every new generation has it, and a jail gets it at its creation when its
// image is older.
const (
	BaseRepoOffPath = "usr/local/etc/pkg/repos/FreeBSD-base.conf"
	BaseRepoOff     = `# Written by JailOps: the base of this jail is upgraded from the host (jailops jail upgrade, jailops jail base add);
# pkg inside the jail handles the ports packages only.
FreeBSD-base: { enabled: no }
`
)

// pkgReposStep writes jailPkgRepos, unless the image already has the file.
func (PkgBase) pkgReposStep(b *Build) plan.Step {
	m, dst := b.M, path.Join(b.Root, "usr/local/etc/pkg/repos/FreeBSD.conf")
	return plan.Step{Name: "write-pkg-repos", Describe: "write " + dst + " (no kernel modules repository: the kernel is the host's) unless it exists",
		Do: func(ctx context.Context) error {
			if ok, err := m.Files.Exists(ctx, dst); err != nil || ok {
				return err
			}
			if err := m.Files.MkdirAll(ctx, path.Dir(dst)); err != nil {
				return err
			}
			return m.Files.WriteAtomic(ctx, dst, []byte(jailPkgRepos))
		}}
}

// baseRepoOffStep writes BaseRepoOff into the root, unless it has the file.
func (PkgBase) baseRepoOffStep(b *Build) plan.Step {
	m, dst := b.M, path.Join(b.Root, BaseRepoOffPath)
	return plan.Step{Name: "write-base-repo-off", Describe: "write " + dst + " (FreeBSD-base stays disabled inside the jail: its base is upgraded from the host) unless it exists",
		Do: func(ctx context.Context) error {
			if ok, err := m.Files.Exists(ctx, dst); err != nil || ok {
				return err
			}
			if err := m.Files.MkdirAll(ctx, path.Dir(dst)); err != nil {
				return err
			}
			return m.Files.WriteAtomic(ctx, dst, []byte(BaseRepoOff))
		}}
}

func (p PkgBase) Upgrade(b *Build) ([]plan.Step, error) {
	r := &pkgRun{p: p, b: b}
	steps := append(append([]plan.Step{p.cacheStep(b), r.abiStep()}, p.keysSteps(b)...), p.repoConf(b))
	return append(steps,
		r.step("pkg-update", "update", "-f", "-r", "jailops"),
		r.step("pkg-upgrade", "upgrade", "-y", "-r", "jailops"),
		r.step("pkg-install", append([]string{"install", "-y", "-r", "jailops"}, p.sets(b.Spec)...)...),
		r.verifyReleaseStep(),
		r.step("pkg-clean", "clean", "-y"),
		// a generation of an image made before JailOps wrote the file gets it with its next generation
		p.homeStep(b), p.baseRepoOffStep(b)), nil
}

func (p PkgBase) Check(b *Build) []plan.Step {
	return []plan.Step{b.M.execStep("pkg-check", p.pkgArgv(b, "", "check", "-d", "-a"))}
}

func (p PkgBase) Packages(ctx context.Context, b *Build) ([]Package, error) {
	out, err := b.M.Exec.Run(ctx, p.pkgArgv(b, "", "query", "%n %v"))
	if err != nil {
		return nil, fmt.Errorf("pkg query: %w: %s", err, tail(out))
	}
	return ParsePkgQuery(out)
}

// ---- Alpine minirootfs ----

// Minirootfs builds Alpine generations from the official minirootfs tarball.
//
// With ImageSpec.Release "3.22.1" the tarball is
// https://dl-cdn.alpinelinux.org/alpine/v3.22/releases/<arch>/alpine-minirootfs-3.22.1-<arch>.tar.gz
// (arch x86_64 for amd64, aarch64 for arm64) and its digest comes from the
// .sha256 file next to it; with "3.22" the newest 3.22.x is looked up in
// latest-releases.yaml, which carries the digest. Source.Repo replaces the
// URL and then needs Source.Keys = "sha256:<hex>"; a pinned Keys always wins
// over the published digest. The download is checked before it is unpacked.
//
// apk does not exist on the FreeBSD host, so every apk command runs inside a
// short-lived build jail around the staging root (Linux mounts, a private
// devfs ruleset, kern.elf64.fallback_brand=3) with `jexec <jail> /sbin/apk`.
// Verified on FreeBSD 15.1 arm64: the layout, apk update and add, and the
// fallback brand. UNVERIFIED: the same driven from the host through the
// helper, and amd64.
type Minirootfs struct{}

func (Minirootfs) Type() string { return "minirootfs" }

// alpineMirror is the default mirror; repositories use https.
const alpineMirror = "https://dl-cdn.alpinelinux.org/alpine"

var alpineArch = map[string]string{"amd64": "x86_64", "arm64": "aarch64"}

func alpineVersionRE(v string) bool {
	parts := strings.Split(v, ".")
	if len(parts) != 2 && len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if _, err := strconv.Atoi(p); err != nil {
			return false
		}
	}
	return true
}

// alpineBranch is "3.22" for "3.22.1".
func alpineBranch(v string) string {
	p := strings.Split(strings.TrimPrefix(v, "v"), ".")
	return p[0] + "." + p[1]
}

func validSHA256(h string) bool {
	if len(h) != 64 {
		return false
	}
	for _, c := range h {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func (Minirootfs) CheckSpec(s api.ImageSpec) error {
	if s.OS != api.OSLinux {
		return api.Errorf(api.CodeInvalid, "minirootfs images are Linux images")
	}
	if alpineArch[s.Arch] == "" {
		return api.Errorf(api.CodeInvalid, "unsupported architecture %q (amd64 or arm64)", s.Arch)
	}
	if s.Source.Repo == "" {
		if !alpineVersionRE(strings.TrimPrefix(s.Release, "v")) {
			return api.Errorf(api.CodeInvalid, "an Alpine image needs release <major>.<minor> or <major>.<minor>.<patch>, for example 3.22.1")
		}
	} else {
		if !strings.HasPrefix(s.Source.Repo, "https://") {
			return api.Errorf(api.CodeInvalid, "minirootfs needs an https tarball URL in source.repo")
		}
		if !alpineVersionRE(strings.TrimPrefix(s.Release, "v")) {
			return api.Errorf(api.CodeInvalid, "an Alpine image needs its release (for example 3.22) for the package repositories")
		}
		if h := strings.TrimPrefix(s.Source.Keys, "sha256:"); h == s.Source.Keys || !validSHA256(h) {
			return api.Errorf(api.CodeInvalid, "a tarball from source.repo needs source.keys = sha256:<64 hex digits>")
		}
	}
	if s.Source.Keys != "" {
		if h := strings.TrimPrefix(s.Source.Keys, "sha256:"); h == s.Source.Keys || !validSHA256(h) {
			return api.Errorf(api.CodeInvalid, "minirootfs source.keys must be sha256:<64 lower-case hex digits>")
		}
	}
	for _, v := range append([]string{s.Source.Repo}, s.Source.Sets...) {
		if err := confValue("source", v); err != nil {
			return err
		}
	}
	for _, p := range s.Source.Sets {
		if p == "" || strings.HasPrefix(p, "-") || strings.ContainsAny(p, " \t/") {
			return api.Errorf(api.CodeInvalid, "invalid apk package name %q in source.sets", p)
		}
	}
	return nil
}

func (Minirootfs) RequiredPaths() []string { return []string{"/bin/busybox", "/lib/apk/db/installed"} }

// ParseLatestReleases finds the minirootfs entry for arch in an Alpine
// latest-releases.yaml (a list of mappings with "file:" and "sha256:").
func ParseLatestReleases(text, arch, branch string) (file, sum string, err error) {
	var cf, cs string
	flush := func() bool {
		return strings.HasPrefix(cf, "alpine-minirootfs-"+branch+".") && strings.HasSuffix(cf, "-"+arch+".tar.gz") && validSHA256(cs)
	}
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "-") {
			if flush() {
				return cf, cs, nil
			}
			cf, cs = "", ""
			t = strings.TrimSpace(strings.TrimPrefix(t, "-"))
		}
		k, v, ok := strings.Cut(t, ":")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), "\"'")
		switch strings.TrimSpace(k) {
		case "file":
			cf = v
		case "sha256":
			cs = v
		}
	}
	if flush() {
		return cf, cs, nil
	}
	return "", "", fmt.Errorf("no alpine-minirootfs %s entry for %s in latest-releases.yaml", branch, arch)
}

// alpineRun is the state the steps of one Alpine plan share.
type alpineRun struct {
	b       *Build
	m       *Manager
	arch    string
	branch  string
	url     string
	want    string // sha256 of the tarball
	tarball string
	jail    string
	ruleset int
	mounted []render.LinuxMount
	jailUp  bool
	rules   bool
}

func newAlpineRun(b *Build) *alpineRun {
	name := "jobuild-" + strings.NewReplacer(".", "-", "_", "-").Replace(b.Image) + "-g" + strconv.Itoa(b.Number)
	return &alpineRun{b: b, m: b.M, arch: alpineArch[b.Spec.Arch], branch: alpineBranch(b.Spec.Release),
		tarball: path.Join(b.Work, "rootfs.tar.gz"), jail: name}
}

func (a *alpineRun) fetch(name, url, dst string) plan.Step {
	return a.m.execStep(name, []string{"fetch", "-o", dst, url})
}

// fetchSteps download and verify the tarball.
func (a *alpineRun) fetchSteps() []plan.Step {
	b, m := a.b, a.m
	rel := strings.TrimPrefix(b.Spec.Release, "v")
	prep := plan.Step{Name: "prepare-work", Describe: "mkdir -p " + b.Work,
		Undo: func(ctx context.Context) error { return m.Files.RemoveAll(ctx, b.Work) },
		Do:   func(ctx context.Context) error { return m.Files.MkdirAll(ctx, b.Work) }}
	var out []plan.Step
	out = append(out, prep)
	pinned := strings.TrimPrefix(b.Spec.Source.Keys, "sha256:")
	if b.Spec.Source.Keys == "" {
		pinned = ""
	}
	switch {
	case b.Spec.Source.Repo != "":
		a.url, a.want = b.Spec.Source.Repo, pinned
	case strings.Count(rel, ".") == 2:
		a.url = fmt.Sprintf("%s/v%s/releases/%s/alpine-minirootfs-%s-%s.tar.gz", alpineMirror, a.branch, a.arch, rel, a.arch)
		a.want = pinned
		if pinned == "" {
			sumFile := path.Join(b.Work, "rootfs.tar.gz.sha256")
			out = append(out, a.fetch("fetch-digest", a.url+".sha256", sumFile), plan.Step{Name: "read-digest", Describe: "read " + sumFile,
				Do: func(ctx context.Context) error {
					d, err := m.Files.ReadFile(ctx, sumFile)
					if err != nil {
						return err
					}
					f := strings.Fields(string(d))
					if len(f) == 0 || !validSHA256(f[0]) {
						return api.Errorf(api.CodeInvalid, "the published digest %s is not a sha256", a.url+".sha256")
					}
					a.want = f[0]
					return nil
				}})
		}
	default:
		idx := path.Join(b.Work, "latest-releases.yaml")
		out = append(out, a.fetch("fetch-index", fmt.Sprintf("%s/v%s/releases/%s/latest-releases.yaml", alpineMirror, a.branch, a.arch), idx),
			plan.Step{Name: "resolve-release", Describe: "pick the newest " + a.branch + " minirootfs from latest-releases.yaml",
				Do: func(ctx context.Context) error {
					d, err := m.Files.ReadFile(ctx, idx)
					if err != nil {
						return err
					}
					file, sum, err := ParseLatestReleases(string(d), a.arch, a.branch)
					if err != nil {
						return api.Errorf(api.CodeInvalid, "%v", err)
					}
					a.url = fmt.Sprintf("%s/v%s/releases/%s/%s", alpineMirror, a.branch, a.arch, file)
					a.want = sum
					if pinned != "" {
						a.want = pinned
					}
					return nil
				}})
	}
	out = append(out, plan.Step{Name: "fetch", Describe: "fetch -o " + a.tarball + " <tarball url>",
		Do: func(ctx context.Context) error {
			argv := []string{"fetch", "-o", a.tarball, a.url}
			if o, err := m.Exec.Run(ctx, argv); err != nil {
				return fmt.Errorf("%s: %w: %s", quote(argv), err, tail(o))
			}
			return nil
		}},
		plan.Step{Name: "verify-digest", Describe: "sha256 of " + a.tarball + " must equal the published or pinned digest",
			Do: func(ctx context.Context) error {
				got, err := m.Files.SHA256(ctx, a.tarball)
				if err != nil {
					return err
				}
				if got != a.want {
					return api.Errorf(api.CodeInvalid, "tarball digest %s does not match %s", got, a.want)
				}
				return nil
			}})
	return out
}

// configSteps write the files apk needs before it can reach the mirror.
func (a *alpineRun) configSteps() []plan.Step {
	b, m := a.b, a.m
	write := func(name, rel string, body func(ctx context.Context) ([]byte, error)) plan.Step {
		dst := path.Join(b.Root, rel)
		return plan.Step{Name: name, Describe: "write " + dst, Do: func(ctx context.Context) error {
			data, err := body(ctx)
			if err != nil {
				return err
			}
			if err := m.Files.MkdirAll(ctx, path.Dir(dst)); err != nil {
				return err
			}
			return m.Files.WriteAtomic(ctx, dst, data)
		}}
	}
	return []plan.Step{
		write("write-resolv-conf", "etc/resolv.conf", func(ctx context.Context) ([]byte, error) {
			d, err := m.Files.ReadFile(ctx, "/etc/resolv.conf")
			if err != nil {
				return nil, fmt.Errorf("the host's /etc/resolv.conf is needed for apk inside the build: %w", err)
			}
			// the marker lets a jail made from the image get its own resolv.conf at its first start
			return append([]byte(render.ResolvMarker+"\n"), d...), nil
		}),
		write("write-apk-repositories", "etc/apk/repositories", func(context.Context) ([]byte, error) {
			return []byte(fmt.Sprintf("%s/v%s/main\n%s/v%s/community\n", alpineMirror, a.branch, alpineMirror, a.branch)), nil
		}),
	}
}

// jailUpSteps brings up the build jail: fallback brand, private devfs ruleset,
// the Linux mounts in order, then the jail itself. Every step undoes itself
// when a later one fails, and teardown is idempotent.
func (a *alpineRun) jailUpSteps() []plan.Step {
	b, m := a.b, a.m
	n := render.BuildDevfsRuleset
	a.ruleset = n
	root := strings.TrimSuffix(b.Root, "/")
	mounts := render.LinuxMounts(root, n, 256<<20)
	steps := []plan.Step{
		{Name: "fallback-brand", Describe: "sysctl kern.elf64.fallback_brand=3 (unbranded ELF runs as Linux; apk installs such binaries)",
			Do: func(ctx context.Context) error {
				o, err := m.Exec.Run(ctx, []string{"sysctl", "kern.elf64.fallback_brand=3"})
				if err != nil {
					return fmt.Errorf("sysctl kern.elf64.fallback_brand=3: %w: %s", err, tail(o))
				}
				return nil
			}},
		{Name: "make-shm-dir", Describe: "mkdir -p " + path.Join(root, render.LinuxShmBacking),
			Do: func(ctx context.Context) error {
				if err := m.Files.MkdirAll(ctx, path.Join(root, render.LinuxCompat)); err != nil {
					return err
				}
				return m.Files.MkdirAll(ctx, path.Join(root, render.LinuxShmBacking))
			}},
	}
	rules, _ := render.LinuxDevfsRuleArgv(n, nil)
	steps = append(steps, plan.Step{Name: "load-devfs-ruleset", Describe: fmt.Sprintf("devfs rule -s %d add ... (private ruleset of the build jail)", n),
		Undo: func(ctx context.Context) error { return a.dropRules(ctx) },
		Do: func(ctx context.Context) error {
			a.rules = true
			for _, r := range rules {
				if o, err := m.Exec.Run(ctx, r); err != nil {
					return fmt.Errorf("%s: %w: %s", quote(r), err, tail(o))
				}
			}
			return nil
		}})
	for _, mt := range mounts {
		mt := mt
		argv := []string{"mount", "-t", mt.Type}
		if mt.Options != "" {
			argv = append(argv, "-o", mt.Options)
		}
		argv = append(argv, mt.Source, mt.Target)
		steps = append(steps, plan.Step{Name: "mount-" + path.Base(mt.Target), Describe: quote(argv),
			Undo: func(ctx context.Context) error { return a.unmount(ctx, mt) },
			Do: func(ctx context.Context) error {
				a.mounted = append(a.mounted, mt)
				if o, err := m.Exec.Run(ctx, argv); err != nil {
					return fmt.Errorf("%s: %w: %s", quote(argv), err, tail(o))
				}
				return nil
			}})
	}
	jargv := []string{"jail", "-c", "name=" + a.jail, "path=" + root, "host.hostname=" + a.jail, "persist",
		"ip4=inherit", "ip6=inherit", fmt.Sprintf("devfs_ruleset=%d", n), "exec.clean"}
	steps = append(steps, plan.Step{Name: "start-build-jail", Describe: quote(jargv),
		Undo: func(ctx context.Context) error { return a.stopJail(ctx) },
		Do: func(ctx context.Context) error {
			if o, err := m.Exec.Run(ctx, jargv); err != nil {
				return fmt.Errorf("%s: %w: %s", quote(jargv), err, tail(o))
			}
			a.jailUp = true
			return nil
		}})
	return steps
}

func (a *alpineRun) stopJail(ctx context.Context) error {
	if !a.jailUp {
		return nil
	}
	a.jailUp = false
	if o, err := a.m.Exec.Run(ctx, []string{"jail", "-r", a.jail}); err != nil {
		return fmt.Errorf("jail -r %s: %w: %s", a.jail, err, tail(o))
	}
	return nil
}

func (a *alpineRun) unmount(ctx context.Context, mt render.LinuxMount) error {
	for i, x := range a.mounted {
		if x == mt {
			a.mounted = append(a.mounted[:i], a.mounted[i+1:]...)
			if o, err := a.m.Exec.Run(ctx, []string{"umount", mt.Target}); err != nil {
				return fmt.Errorf("umount %s: %w: %s", mt.Target, err, tail(o))
			}
			return nil
		}
	}
	return nil
}

func (a *alpineRun) dropRules(ctx context.Context) error {
	if !a.rules {
		return nil
	}
	a.rules = false
	if o, err := a.m.Exec.Run(ctx, []string{"devfs", "rule", "-s", strconv.Itoa(a.ruleset), "delset"}); err != nil {
		return fmt.Errorf("devfs rule delset: %w: %s", err, tail(o))
	}
	return nil
}

// apk runs an apk command inside the build jail.
func (a *alpineRun) apk(name string, args ...string) plan.Step {
	argv := append([]string{"jexec", a.jail, "/sbin/apk", "--no-interactive"}, args...)
	return a.m.execStep(name, argv)
}

// tearDown stops the jail, unmounts in reverse order and drops the ruleset.
func (a *alpineRun) tearDown() plan.Step {
	return plan.Step{Name: "tear-down-build-jail", Describe: "jail -r " + a.jail + "; umount the Linux mounts in reverse; devfs rule delset",
		Do: func(ctx context.Context) error {
			if err := a.stopJail(ctx); err != nil {
				return err
			}
			for len(a.mounted) > 0 {
				if err := a.unmount(ctx, a.mounted[len(a.mounted)-1]); err != nil {
					return err
				}
			}
			return a.dropRules(ctx)
		}}
}

func (a *alpineRun) apkSteps(upgrade bool) []plan.Step {
	steps := a.jailUpSteps()
	steps = append(steps, a.apk("apk-update", "update"))
	if upgrade {
		steps = append(steps, a.apk("apk-upgrade", "upgrade"))
	}
	if sets := a.b.Spec.Source.Sets; len(sets) > 0 {
		steps = append(steps, a.apk("apk-add", append([]string{"add"}, sets...)...))
	}
	steps = append(steps, a.apk("apk-verify", "verify"), a.tearDown())
	return steps
}

func (Minirootfs) Install(b *Build) ([]plan.Step, error) {
	a := newAlpineRun(b)
	steps := a.fetchSteps()
	steps = append(steps, plan.Step{Name: "extract", Describe: "tar -xpf " + a.tarball + " -C " + b.Root,
		Do: func(ctx context.Context) error {
			argv := []string{"tar", "-C", b.Root, "-xpzf", a.tarball}
			if o, err := b.M.Exec.Run(ctx, argv); err != nil {
				return fmt.Errorf("%s: %w: %s", quote(argv), err, tail(o))
			}
			return nil
		}})
	steps = append(steps, a.configSteps()...)
	return append(steps, a.apkSteps(false)...), nil
}

func (Minirootfs) Upgrade(b *Build) ([]plan.Step, error) {
	a := newAlpineRun(b)
	steps := a.configSteps()
	return append(steps, a.apkSteps(true)...), nil
}

// Check has nothing left to do: apk verify ran inside the build jail, before
// it was torn down.
func (Minirootfs) Check(*Build) []plan.Step { return nil }

func (Minirootfs) Packages(ctx context.Context, b *Build) ([]Package, error) {
	db, err := b.M.Files.ReadFile(ctx, path.Join(b.Root, "lib/apk/db/installed"))
	if err != nil {
		return nil, err
	}
	return ParseApkInstalled(db)
}

// ---- Debian debootstrap ----

// Debootstrap builds Debian generations with debootstrap(8). Source.Repo is the
// mirror, ImageSpec.Release the suite, Source.Keys an optional keyring path.
// Upgrading a Debian generation needs a chroot with Linux mounts and is not
// implemented: it returns api.CodeUnsupported.
// The release signature is always checked: a keyring is required.
type Debootstrap struct{}

func (Debootstrap) Type() string { return "debootstrap" }

func (Debootstrap) CheckSpec(s api.ImageSpec) error {
	if s.OS != api.OSLinux {
		return api.Errorf(api.CodeInvalid, "debootstrap images are Linux images")
	}
	if s.Release == "" || strings.HasPrefix(s.Release, "-") || strings.ContainsAny(s.Release, " /") {
		return api.Errorf(api.CodeInvalid, "debootstrap needs a suite name in release")
	}
	if s.Arch == "" {
		return api.Errorf(api.CodeInvalid, "arch is required")
	}
	if s.Source.Keys == "" {
		return api.Errorf(api.CodeInvalid, "debootstrap needs source.keys: the keyring that checks the release signature (for Debian, debian-archive-keyring.gpg)")
	}
	if s.Source.Repo != "" && !strings.HasPrefix(s.Source.Repo, "https://") {
		return api.Errorf(api.CodeInvalid, "the debootstrap mirror must be an https URL")
	}
	return nil
}

func (Debootstrap) RequiredPaths() []string { return []string{"/bin/sh", "/var/lib/dpkg/status"} }

func (Debootstrap) Install(b *Build) ([]plan.Step, error) {
	argv := []string{"debootstrap", "--arch=" + b.Spec.Arch, "--variant=minbase", "--keyring=" + b.Spec.Source.Keys, b.Spec.Release, b.Root}
	mirror := b.Spec.Source.Repo
	if mirror == "" {
		mirror = "https://deb.debian.org/debian" // debootstrap's own default is plain http
	}
	argv = append(argv, mirror)
	run := b.M.execStep("debootstrap", argv)
	// debootstrap mounts proc (and on some versions dev) inside the target while it works; a failed or killed run
	// can leave them, and the build root cannot be unmounted or rolled back while they are there.
	do := run.Do
	root, ex := b.Root, b.M.Exec
	run.Do = func(ctx context.Context) error {
		defer func() {
			for _, sub := range []string{"dev/pts", "dev", "proc", "sys"} {
				_, _ = ex.Run(ctx, []string{"umount", path.Join(root, sub)}) // not mounted is fine
			}
		}()
		return do(ctx)
	}
	return []plan.Step{run}, nil
}

func (Debootstrap) Upgrade(*Build) ([]plan.Step, error) {
	return nil, api.Errorf(api.CodeUnsupported, "upgrading a debootstrap generation is not implemented; build from the source instead")
}

func (Debootstrap) Check(*Build) []plan.Step { return nil }

func (Debootstrap) Packages(ctx context.Context, b *Build) ([]Package, error) {
	db, err := b.M.Files.ReadFile(ctx, path.Join(b.Root, "var/lib/dpkg/status"))
	if err != nil {
		return nil, err
	}
	return ParseDpkgStatus(db)
}
