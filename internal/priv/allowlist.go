package priv

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"jailops.net/jailops/internal/jailaudit"
	"jailops.net/jailops/internal/render"
)

// The command allow-list for Runner. lifecycle needs pkg(8) to work on a jail root from the outside. It is
// described by one Rule below (as are the other host tools); a command that
// matches no rule is refused, and the binary that runs is the rule's fixed
// absolute path, never a path the caller names.
//
// UNVERIFIED: the flags are those lifecycle uses; they have not been run
// against FreeBSD 15 tools.

// pathKind says which paths a flag value or argument may hold.
type pathKind int

const (
	pathJailTree pathKind = iota + 1 // below a storage root's mountpoint, not the mountpoint itself
	pathState                        // below the work directory or a storage root's mountpoint
	pathJailLog                      // the log directory of a jail root, or a plain name at most two levels below it (logs.go)
)

// flagRule describes one flag.
type flagRule struct {
	Flag  string
	Value *regexp.Regexp // value pattern, nil for a flag without a value
	Path  pathKind       // when set the value is a path of this kind
	Many  bool           // may repeat
	// Check validates a value that needs more than a pattern or a path.
	Check func(c Config, v string) error
}

// Rule is one entry of the allow-list.
type Rule struct {
	// Name is argv[0] as the caller sends it.
	Name string
	// Binary is the absolute path that runs.
	Binary string
	// Flags are the accepted flags; they must come before the subcommand.
	Flags []flagRule
	// RequiredFlags must all appear.
	RequiredFlags []string
	// OneOfFlags: exactly one of these must appear.
	OneOfFlags []string
	// Subcommands are the accepted first positional words. Empty means none
	// are accepted and the rule takes no positionals unless Rest is set.
	Subcommands []string
	// SubcommandOptional lets the command run without a subcommand.
	SubcommandOptional bool
	// Rest matches the arguments after the subcommand, one by one.
	Rest *regexp.Regexp
	// RestCheck is a further test of each of them.
	RestCheck func(string) bool
	// MaxRest bounds their number.
	MaxRest int
	// RestFlags are flags allowed after the subcommand.
	RestFlags []string
	// MaxTimeoutSeconds caps the command's runtime.
	MaxTimeoutSeconds int
	// Exact lists whole argument vectors (after the command name) accepted
	// as they are, besides what the flag rules describe.
	Exact [][]string
	// RestExact lists argument vectors after the flags (subcommand and its
	// arguments) accepted as they are once the flags passed their rules: a
	// read-only form whose arguments the generic Rest pattern does not take.
	RestExact [][]string
	// Reason says why the host tool is reachable at all.
	Reason string
	// Env, when set, is the whole environment the command runs with (the caller's is never used). A tool
	// that is itself a script needs the ports tree's directories in PATH.
	Env []string
	// Custom, when set, replaces the generic parsing: it receives every
	// argument after the command name and either accepts or refuses them.
	// Admin is true when the request carries the admin assertion.
	Custom func(c Config, admin bool, args []string) error
	// Verify runs in the helper after Custom accepted the arguments and
	// checks what needs the host: that a named jail has a conf file, that an
	// archive has no hostile member. The arguments are those after the
	// command name.
	Verify func(ctx context.Context, l *Local, args []string) error
}

var (
	reRelease = regexp.MustCompile(`^[0-9]{1,2}\.[0-9]{1,2}-[A-Za-z0-9_]{1,16}$`)
	// rePkgName also takes the format of "pkg query" (%n-%v).
	rePkgName = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._+@:=/-]{0,127}|%[a-zA-Z][%a-zA-Z0-9._ :-]{0,60})$`)
	reABI     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:._-]{0,39}$`)
)

// KldModules are the kernel modules kldload and kldstat accept.
var KldModules = []string{"pf", "if_epair", "if_wg", "linux", "linux64", "linprocfs", "linsysfs", "fdescfs", "nullfs", "tmpfs", "zfs", "dummynet"}

// SysctlWrites are the only sysctls a write is accepted for, each with the
// pattern its value must match. Writing needs the admin assertion.
var SysctlWrites = map[string]*regexp.Regexp{
	"net.inet6.ip6.forwarding": regexp.MustCompile(`^[01]$`),
	"net.inet.ip.forwarding":   regexp.MustCompile(`^[01]$`),
	// -1 or 3 only: unbranded ELF binaries run as Linux (3) or are refused (-1).
	"kern.elf64.fallback_brand": regexp.MustCompile(`^(-1|3)$`),
}

var pfctlShow = []string{"info", "rules", "nat", "memory", "Anchors", "Tables"}

// customSysctl accepts "-n <readable name>" and, for an admin,
// "<writable name>=<value>".
func customSysctl(c Config, admin bool, args []string) error {
	switch {
	case len(args) == 2 && args[0] == "-n":
		if !c.sysctlReadable(args[1]) {
			return forbidden("sysctl %q is not on the allow-list", args[1])
		}
		return nil
	case len(args) == 1 && !strings.HasPrefix(args[0], "-"):
		name, val, ok := strings.Cut(args[0], "=")
		re := SysctlWrites[name]
		if !ok || re == nil || !sysctlNameRE.MatchString(name) {
			return forbidden("sysctl %q cannot be written through the helper", args[0])
		}
		if !admin {
			return forbidden("writing sysctl %s needs the admin assertion", name)
		}
		if !re.MatchString(val) {
			return invalid("invalid value %q for %s", val, name)
		}
		return nil
	}
	return forbidden("sysctl: only -n <name> and <name>=<value> are accepted")
}

// sysctlReadable reports whether name is on the read allow-list.
func (c Config) sysctlReadable(name string) bool {
	if !sysctlNameRE.MatchString(name) {
		return false
	}
	_, w := SysctlWrites[name]
	return w || slices.Contains(DefaultSysctls, name) || slices.Contains(c.Sysctls, name)
}

func customKldstat(_ Config, _ bool, args []string) error {
	if len(args) == 3 && args[0] == "-q" { // -q: no output, the exit status answers
		args = args[1:]
	}
	if len(args) != 2 || args[0] != "-n" || !slices.Contains(KldModules, args[1]) {
		return forbidden("kldstat: only -n <module> for an allow-listed module is accepted")
	}
	return nil
}

func customKldload(_ Config, _ bool, args []string) error {
	if len(args) != 1 || !slices.Contains(KldModules, args[0]) {
		return forbidden("kldload: only an allow-listed module is accepted")
	}
	return nil
}

func customUname(_ Config, _ bool, args []string) error {
	if len(args) != 1 || (args[0] != "-r" && args[0] != "-m" && args[0] != "-s") {
		return forbidden("uname: only -r, -m or -s is accepted")
	}
	return nil
}

// customRctl accepts only "-u jail:<key>": the resource usage of one jail
// named by its key. Rules (-a, -r, -l) and other subjects are refused.
func customRctl(_ Config, _ bool, args []string) error {
	if len(args) != 2 || args[0] != "-u" || !strings.HasPrefix(args[1], "jail:") {
		return forbidden("rctl: only -u jail:<jail> is accepted")
	}
	return ValidJailName(strings.TrimPrefix(args[1], "jail:"))
}

func verifyRctl(_ context.Context, l *Local, args []string) error {
	return jailGuard{l}.managed(strings.TrimPrefix(args[1], "jail:"))
}

var reDnRate = regexp.MustCompile(`^[1-9][0-9]{0,11}bit/s$`)

// customDnctl accepts "pipe N config bw RATE" (RATE is 0, unlimited, or a number of bit/s), "pipe delete N" and
// "pipe list", for a pipe number in JailOps's own range. Queues, schedulers and every other pipe parameter are refused.
func customDnctl(_ Config, _ bool, args []string) error {
	num := func(s string) (int, bool) {
		n, err := strconv.Atoi(s)
		return n, err == nil && strconv.Itoa(n) == s && render.IsManagedPipe(n)
	}
	switch {
	case len(args) == 5 && args[0] == "pipe" && args[2] == "config" && args[3] == "bw":
		if _, ok := num(args[1]); !ok {
			return forbidden("dnctl: pipe %q is not a JailOps pipe (%d to %d)", args[1], render.PipeBase, render.PipeBase+render.PipeSpan-1)
		}
		if args[4] != "0" && !reDnRate.MatchString(args[4]) {
			return forbidden("dnctl: bandwidth %q must be 0 or a number followed by bit/s", args[4])
		}
		return nil
	case len(args) == 3 && args[0] == "pipe" && args[1] == "delete":
		if _, ok := num(args[2]); !ok {
			return forbidden("dnctl: pipe %q is not a JailOps pipe (%d to %d)", args[2], render.PipeBase, render.PipeBase+render.PipeSpan-1)
		}
		return nil
	case len(args) == 2 && args[0] == "pipe" && args[1] == "list":
		return nil
	}
	return forbidden("dnctl: only pipe N config bw RATE, pipe delete N and pipe list are accepted")
}

// customPfctl accepts the read-only forms "-s info|rules|nat|memory" and
// "-a <jailops anchor> -s rules|nat|Anchors|Tables|info", flags in any order.
func customPfctl(_ Config, _ bool, args []string) error {
	var anchor, what string
	anchorSet := false
	for i := 0; i < len(args); i += 2 {
		if i+1 >= len(args) {
			return invalid("pfctl: %s needs a value", args[i])
		}
		switch args[i] {
		case "-a":
			if anchor != "" {
				return invalid("pfctl: -a given twice")
			}
			anchor = args[i+1]
			anchorSet = true
		case "-s":
			if what != "" {
				return invalid("pfctl: -s given twice")
			}
			what = args[i+1]
		default:
			return forbidden("pfctl: flag %q is not allowed", args[i])
		}
	}
	if !slices.Contains(pfctlShow, what) {
		return forbidden("pfctl: -s must be one of %s", strings.Join(pfctlShow, ", "))
	}
	if !anchorSet {
		// The main ruleset is read-only state too: doctor looks for the jailops anchor lines in it.
		if what != "info" && what != "rules" && what != "nat" && what != "memory" { // memory: the limits (doctor: pf.states)
			return forbidden("pfctl -s %s needs -a <jailops anchor>", what)
		}
		return nil
	}
	if anchor == strings.TrimSuffix(AnchorPrefix, "/") {
		return nil
	}
	return checkAnchor(anchor)
}

var pkgOptionValue = map[string]string{"ASSUME_ALWAYS_YES": "yes", "IGNORE_OSVERSION": "yes", "RUN_SCRIPTS": "false", "PKG_ENABLE_PLUGINS": "false"}

// reOSVersion is a __FreeBSD_version (kern.osreldate): pkg refuses packages built for a newer one.
var reOSVersion = regexp.MustCompile(`^[1-9][0-9]{6}$`)

// checkPkgOption accepts the few -o options image building, the shell setup and the upgrade of a jail need:
// REPOS_DIR=<path under the storage roots or the state directory>, PKG_CACHEDIR=<the same>,
// ASSUME_ALWAYS_YES=yes, IGNORE_OSVERSION=yes, ABI=<abi>, OSVERSION=<__FreeBSD_version>, RUN_SCRIPTS=false,
// PKG_ENABLE_PLUGINS=false, and the limits of a download: FETCH_TIMEOUT=<1 to 600 seconds> and FETCH_RETRY=<0 to 10>.
func checkPkgJail(_ Config, v string) error { return ValidJailName(v) }

func checkPkgOption(c Config, v string) error {
	k, val, ok := strings.Cut(v, "=")
	if !ok {
		return forbidden("pkg -o %q is not allowed", v)
	}
	switch {
	case k == "REPOS_DIR", k == "PKG_CACHEDIR":
		return c.checkRulePath(pathState, val)
	case k == "ABI" && reABI.MatchString(val), k == "OSVERSION" && reOSVersion.MatchString(val):
		return nil
	case k == "FETCH_TIMEOUT" && inRange(val, 1, 600), k == "FETCH_RETRY" && inRange(val, 0, 10):
		return nil
	case pkgOptionValue[k] != "" && pkgOptionValue[k] == val:
		return nil
	}
	return forbidden("pkg -o %q is not allowed", v)
}

// inRange says whether v is a plain decimal number from lo to hi.
func inRange(v string, lo, hi int) bool {
	n, err := strconv.Atoi(v)
	return err == nil && strconv.Itoa(n) == v && n >= lo && n <= hi
}

// pkgNameOK refuses what rePkgName lets through and pkg would take as a file: a name that climbs out (..), one that ends
// as a package or an archive, and more than one slash (a port origin has one: category/name).
func pkgNameOK(a string) bool {
	if strings.Contains(a, "..") || strings.Count(a, "/") > 1 {
		return false
	}
	for _, suf := range []string{".pkg", ".txz", ".tbz", ".tgz", ".tar", ".gz", ".xz", ".zst"} {
		if strings.HasSuffix(strings.ToLower(a), suf) {
			return false
		}
	}
	return true
}

// customPwdMkdb accepts only: pwd_mkdb -p -d <etc dir> <master.passwd>.
// customPwLockRoot accepts exactly `pw -R <tree> usermod root -w no`: the tree is a jail tree (an image root being built)
// and the only change is that root has no password to log in with.
func customPwLockRoot(c Config, _ bool, args []string) error {
	if len(args) != 6 || args[0] != "-R" || args[2] != "usermod" || args[3] != "root" || args[4] != "-w" || args[5] != "no" {
		return forbidden("pw: only -R <image root> usermod root -w no is accepted")
	}
	return c.checkRulePath(pathJailTree, args[1])
}

func customPwdMkdb(c Config, _ bool, args []string) error {
	if len(args) != 4 || args[0] != "-p" || args[1] != "-d" {
		return forbidden("pwd_mkdb: only -p -d <directory> <master.passwd> is accepted")
	}
	if err := c.checkRulePath(pathJailTree, args[2]); err != nil {
		return err
	}
	if err := c.checkRulePath(pathState, args[3]); err != nil {
		return err
	}
	if filepath.Base(args[3]) != "master.passwd" {
		return forbidden("pwd_mkdb: the file must be named master.passwd")
	}
	return nil
}

// recordNameOK is a package name as pkg prints it (FreeBSD-runtime, bash): no glob, no regexp, no file.
func recordNameOK(a string) bool {
	return rePkgName.MatchString(a) && pkgNameOK(a) && !strings.ContainsAny(a, "*?[]{}") && !strings.HasPrefix(a, "-")
}

// CommandRules is the single table of commands Runner may execute.
var CommandRules = []Rule{
	{
		Name: "sysctl", Binary: "/sbin/sysctl", MaxTimeoutSeconds: 10, Custom: customSysctl,
		Reason: "read allow-listed kernel state (-n) and, for an admin, set the forwarding switches routed vnet networking needs",
	},
	{
		Name: "kldstat", Binary: "/sbin/kldstat", MaxTimeoutSeconds: 10, Custom: customKldstat,
		Reason: "ask whether a kernel module JailOps depends on is loaded",
	},
	{
		Name: "kldload", Binary: "/sbin/kldload", MaxTimeoutSeconds: 30, Custom: customKldload,
		Reason: "load one of the fixed kernel modules (pf, epair, linuxulator, nullfs, tmpfs, zfs...) a feature needs",
	},
	{
		Name: "pfctl", Binary: "/sbin/pfctl", MaxTimeoutSeconds: 20, Custom: customPfctl,
		Reason: "read-only state of pf and of the jailops anchors (rulesets are loaded through the pf.* operations instead)",
	},
	{
		Name: "fetch", Binary: "/usr/bin/fetch", MaxTimeoutSeconds: 1800, Custom: customFetch,
		Reason: "download an image tarball or key from an allow-listed https host (fetch-host) into the state or storage area",
	},
	{
		Name: "debootstrap", Binary: "/usr/local/sbin/debootstrap", MaxTimeoutSeconds: 3600, Custom: customDebootstrap,
		Env:    []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "LC_ALL=C", "HOME=/var/empty"},
		Reason: "build a Debian or Ubuntu root filesystem in an image build root, from a mirror, with the release signature checked against a keyring",
	},
	{
		Name: "tar", Binary: "/usr/bin/tar", MaxTimeoutSeconds: 900, Custom: customTar, Verify: verifyTar,
		Reason: "list, and after the member names were checked extract, a downloaded root filesystem archive into an image root",
	},
	{
		Name: "chmod", Binary: "/bin/chmod", MaxTimeoutSeconds: 60, Custom: customChmod, Verify: verifyChmod,
		Reason: "set the mode of files in an image root (never setuid or setgid)",
	},
	{
		Name: "mount", Binary: "/sbin/mount", MaxTimeoutSeconds: 60, Custom: customMount, Verify: verifyMount,
		Reason: "mount nullfs, tmpfs, devfs, fdescfs, linprocfs and linsysfs below the root of a managed jail",
	},
	{
		Name: "umount", Binary: "/sbin/umount", MaxTimeoutSeconds: 60, Custom: customUmount, Verify: verifyUmount,
		Reason: "unmount what mount mounted, below the root of a managed jail",
	},
	{
		Name: "jail", Binary: "/usr/sbin/jail", MaxTimeoutSeconds: 120, Custom: customJail, Verify: verifyJail,
		Reason: "create or remove a jail from its validated conf file (the build jail of an image)",
	},
	{
		Name: "devfs", Binary: "/sbin/devfs", MaxTimeoutSeconds: 30, Custom: customDevfs,
		Reason: "define the private devfs ruleset of a jail (a small rule grammar, rulesets 100 and up), and read the stock rulesets 1 to 4 (showsets, show)",
	},
	{
		Name: "jexec", Binary: "/usr/sbin/jexec", MaxTimeoutSeconds: 1800, Custom: customJexec, Verify: verifyJexec,
		Reason: "run apk inside a managed Alpine jail to provision it (interactive sessions use the exec operation instead)",
	},
	{
		Name: "ifconfig", Binary: "/sbin/ifconfig", MaxTimeoutSeconds: 30, Custom: customIfconfig, Verify: verifyIfconfig,
		Reason: "configure the interface of a managed jail from the host (ifconfig -j); epairs of the host are made by the typed net.* operations",
	},
	{
		Name: "route", Binary: "/sbin/route", MaxTimeoutSeconds: 30, Custom: customRoute, Verify: verifyRoute,
		Reason: "add or delete the default or a host route inside the vnet of a managed jail (route -j)",
	},
	{
		Name: "uname", Binary: "/usr/bin/uname", MaxTimeoutSeconds: 5, Custom: customUname,
		Reason: "report the running release, architecture and kernel name",
	},
	{
		Name: "rctl", Binary: "/usr/bin/rctl", MaxTimeoutSeconds: 10, Custom: customRctl, Verify: verifyRctl,
		Reason: "read the resource accounting (processes, cpu time, memory) of a managed jail; rules are neither read nor set",
	},
	{
		Name: "dnctl", Binary: "/sbin/dnctl", MaxTimeoutSeconds: 10, Custom: customDnctl,
		Reason: "configure and delete the dummynet pipes of the bandwidth limits, in JailOps's own range of pipe numbers only",
	},
	{
		Name: "pkg", Binary: "/usr/sbin/pkg", Reason: "build images and upgrade the root of a jail from the host (-r), or install and update packages inside a running jail (-j)",
		Verify: verifyPkg,

		Flags:      []flagRule{{Flag: "-r", Path: pathJailTree}, {Flag: "-j", Check: checkPkgJail}, {Flag: "-o", Check: checkPkgOption, Many: true}},
		OneOfFlags: []string{"-r", "-j"},
		Exact:      [][]string{{"config", "abi"}},
		RestExact: [][]string{
			// the catalogue of the private repository, read into a scratch root: what a jail's upgrade compares with
			{"rquery", "-r", "jailops", "%n %v %sb"},
			// the audit of a jail: the packages of a copy of its database against the host's vulnerability database,
			// which pkg reads and never fetches here (no -F)
			{"audit", "-f", jailaudit.HostVulnXML}},
		Subcommands: []string{"install", "upgrade", "update", "delete", "remove", "autoremove", "info", "audit", "lock", "unlock", "fetch", "clean", "check", "version", "bootstrap", "query"},
		Rest:        rePkgName, RestCheck: pkgNameOK, MaxRest: 64, RestFlags: []string{"-y", "-q", "-n", "-f", "-a", "-U", "-F", "-x", "-r", "-d", "-s"},
		MaxTimeoutSeconds: 3600,
	},
	{
		Name: "pwd_mkdb", Binary: "/usr/sbin/pwd_mkdb", MaxTimeoutSeconds: 60, Custom: customPwdMkdb,
		Reason: "rebuild the password databases of an image root after its master.passwd changed",
	},
	{
		Name: "pw-lock-root", Binary: "/usr/sbin/pw", MaxTimeoutSeconds: 60, Custom: customPwLockRoot,
		Reason: "lock the password of root in the new root of an image (the base ships root with an empty one): pw -R <image root> usermod root -w no, and nothing else",
	},
	{
		Name: "freebsd-version", Binary: "/bin/freebsd-version", MaxTimeoutSeconds: 10,
		Exact:  [][]string{{"-k"}, {"-u"}},
		Reason: "report the kernel and the userland version of the host",
	},
	{
		Name: "netstat", Binary: "/usr/bin/netstat", MaxTimeoutSeconds: 10,
		Exact:  [][]string{{"-s", "-p", "icmp6"}},
		Reason: "read the ICMPv6 counters of the host (doctor counts the neighbor solicitations the kernel rejected); no other form is accepted",
	},
}

// ruleFor returns the rule for a command name.
func ruleFor(name string) *Rule {
	for i := range CommandRules {
		if CommandRules[i].Name == name {
			return &CommandRules[i]
		}
	}
	return nil
}

// checkPath validates a path argument of the given kind.
func (c Config) checkRulePath(kind pathKind, p string) error {
	if !cleanAbs(p) || len(p) > maxPath {
		return forbidden("path %q is not a clean absolute path", p)
	}
	switch kind {
	case pathJailLog:
		// Lexical only: the read opens the directory as a root of its own and
		// refuses every symlink below the jail root itself.
		_, err := c.logRef(p)
		return err
	case pathJailTree:
		if c.isMountRoot(p) || !c.inMounts(p) {
			return c.notBelowMount(p)
		}
	case pathState:
		if !c.inWork(p) && !c.inMounts(p) {
			return forbidden("path %q is outside the state directory", p)
		}
	}
	// Resolve symlinks for paths that exist: the final target must still be
	// inside the allowed roots.
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		// A file that does not exist yet (a log, a work tree): its directory
		// must resolve inside the roots.
		if pr, perr := filepath.EvalSymlinks(filepath.Dir(p)); perr == nil {
			real, err = filepath.Join(pr, filepath.Base(p)), nil
		}
	}
	if err == nil {
		var mountReal []string
		for _, m := range c.reg.mounts() {
			if r, err := filepath.EvalSymlinks(m); err == nil {
				m = r
			}
			mountReal = append(mountReal, m)
		}
		inMount := false
		for _, m := range mountReal {
			inMount = inMount || within(m, real)
		}
		workOK := false
		if kind == pathState && c.WorkDir != "" {
			wr := c.WorkDir
			if r, err := filepath.EvalSymlinks(wr); err == nil {
				wr = r
			}
			workOK = within(wr, real)
		}
		if !inMount && !workOK {
			return forbidden("path %q resolves outside the allowed roots", p)
		}
		if kind == pathJailTree && slices.Contains(mountReal, real) {
			return forbidden("path %q resolves to a storage root", p)
		}
		// below a jail's tree, the target stays in that tree (a jail's symlink cannot point a command at another jail)
		if r := c.inJailTree(c.mountRel(p)); r.base != "" {
			tr := r.root
			if t, err := filepath.EvalSymlinks(tr); err == nil {
				tr = t
			}
			if !within(tr, real) {
				return forbidden("path %q resolves outside the jail's tree %s", p, r.root)
			}
		}
	}
	return nil
}

// CheckCommand validates argv against CommandRules and returns the rule and
// the argv to run (argv[0] replaced with the fixed binary).
func (c Config) CheckCommand(argv []string) (*Rule, []string, error) {
	return c.CheckCommandCtx(context.Background(), argv)
}

// CheckCommandCtx is CheckCommand for a request: the admin assertion in ctx
// unlocks the rules that need it.
func (c Config) CheckCommandCtx(ctx context.Context, argv []string) (*Rule, []string, error) {
	if len(argv) == 0 || argv[0] == "" {
		return nil, nil, invalid("empty command")
	}
	if len(argv) > c.Limits.MaxArgv {
		return nil, nil, invalid("too many arguments")
	}
	total := 0
	for _, a := range argv {
		total += len(a) + 1
		if strings.ContainsRune(a, 0) || !utf8.ValidString(a) {
			return nil, nil, invalid("invalid argument")
		}
	}
	if total > c.Limits.MaxArgvBytes {
		return nil, nil, invalid("command line too long")
	}
	r := ruleFor(argv[0])
	if r == nil {
		return nil, nil, forbidden("command %q is not on the allow-list", argv[0])
	}
	args := argv[1:]
	for _, ex := range r.Exact {
		if slices.Equal(ex, args) {
			return r, append([]string{binaryOf(r)}, args...), nil
		}
	}
	if r.Custom != nil {
		if err := r.Custom(c, isAdmin(ctx), args); err != nil {
			return nil, nil, err
		}
		return r, append([]string{binaryOf(r)}, args...), nil
	}
	seen := map[string]int{}
	i := 0
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		var fr *flagRule
		for k := range r.Flags {
			if r.Flags[k].Flag == args[i] {
				fr = &r.Flags[k]
			}
		}
		if fr == nil {
			break
		}
		seen[fr.Flag]++
		if seen[fr.Flag] > 1 && !fr.Many {
			return nil, nil, invalid("%s given twice", fr.Flag)
		}
		i++
		if fr.Value == nil && fr.Path == 0 && fr.Check == nil {
			continue
		}
		if i >= len(args) {
			return nil, nil, invalid("%s needs a value", fr.Flag)
		}
		v := args[i]
		i++
		if fr.Check != nil {
			if err := fr.Check(c, v); err != nil {
				return nil, nil, err
			}
		} else if fr.Path != 0 {
			if err := c.checkRulePath(fr.Path, v); err != nil {
				return nil, nil, err
			}
		} else if !fr.Value.MatchString(v) {
			return nil, nil, invalid("invalid value %q for %s", v, fr.Flag)
		}
	}
	for _, f := range r.RequiredFlags {
		if seen[f] == 0 {
			return nil, nil, invalid("%s needs %s", r.Name, f)
		}
	}
	if len(r.OneOfFlags) > 0 {
		n := 0
		for _, f := range r.OneOfFlags {
			n += seen[f]
		}
		if n != 1 {
			return nil, nil, invalid("%s needs exactly one of %s", r.Name, strings.Join(r.OneOfFlags, " and "))
		}
	}
	rest := args[i:]
	for _, ex := range r.RestExact {
		if slices.Equal(ex, rest) {
			return r, append([]string{binaryOf(r)}, args...), nil
		}
	}
	if len(rest) == 0 {
		if !r.SubcommandOptional {
			return nil, nil, invalid("%s needs a subcommand", r.Name)
		}
	} else {
		if !slices.Contains(r.Subcommands, rest[0]) {
			return nil, nil, forbidden("%s: argument %q is not allowed here", r.Name, rest[0])
		}
		rest = rest[1:]
		if len(rest) > r.MaxRest {
			return nil, nil, invalid("%s: too many arguments", r.Name)
		}
		for _, a := range rest {
			if strings.HasPrefix(a, "-") {
				if !slices.Contains(r.RestFlags, a) {
					return nil, nil, forbidden("%s: flag %q is not allowed", r.Name, a)
				}
				continue
			}
			if r.Rest == nil || !r.Rest.MatchString(a) || (r.RestCheck != nil && !r.RestCheck(a)) {
				return nil, nil, forbidden("%s: argument %q is not allowed", r.Name, a)
			}
		}
	}
	out := append([]string{binaryOf(r)}, args...)
	return r, out, nil
}

// inWork reports whether p is below the daemon's scratch directory.
func (c Config) inWork(p string) bool { return c.WorkDir != "" && within(c.WorkDir, p) }

// pkgReal is the pkg program itself. /usr/sbin/pkg is only the program that fetches it: a newer one looks for pkg inside the root of
// -r and refuses ("the package management tool is not yet installed") when the root is a jail tree that has none, which is what an
// image build and a jail root are. The program itself takes -r and -j as they are.
var pkgReal = "/usr/local/sbin/pkg"

// pkgProgram is pkg itself when it is installed and the program that fetches it otherwise.
func pkgProgram() string {
	if _, err := os.Stat(pkgReal); err == nil {
		return pkgReal
	}
	return "/usr/sbin/pkg"
}

// binaryOf is the program that runs for r: its Binary, except that pkg runs as pkg itself when it is installed.
func binaryOf(r *Rule) string {
	if r.Name == "pkg" {
		return pkgProgram()
	}
	return r.Binary
}
