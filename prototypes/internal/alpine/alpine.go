// Package alpine holds the pure logic of prototype 6 (Alpine under the Linuxulator in a
// jail). Nothing here touches the system, so it is unit-tested on Linux.
//
// Nothing in this package or its caller has been run on FreeBSD by the author.
package alpine

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Fixed names. All carry the mandatory "jop" prefix.
const (
	JailName = "jop6alpine"
	Bridge   = "jopbr6"
	EpairA   = "jop6h" // host side, member of the bridge
	EpairB   = "jop6j" // jail side, moved into the vnet
	StateDir = "/var/tmp/jop-proto-6"

	HostAddr4 = "10.66.6.1"
	JailAddr4 = "10.66.6.2"
	HostAddr6 = "fd66:6:6::1"
	JailAddr6 = "fd66:6:6::2"

	// DefaultRuleset is a deliberately high devfs ruleset number; the operator may override it.
	DefaultRuleset = 6600
)

// ---- minirootfs selection -------------------------------------------------------------

// Release is one minirootfs entry from latest-releases.yaml.
type Release struct {
	File   string
	SHA256 string
}

// LatestReleasesURL is the index for a branch such as "v3.22".
func LatestReleasesURL(branch, arch string) string {
	return fmt.Sprintf("https://dl-cdn.alpinelinux.org/alpine/%s/releases/%s/latest-releases.yaml", branch, arch)
}

// ReleaseURL is the download URL of a file of a branch.
func ReleaseURL(branch, arch, file string) string {
	return fmt.Sprintf("https://dl-cdn.alpinelinux.org/alpine/%s/releases/%s/%s", branch, arch, file)
}

// ParseLatestReleases finds the minirootfs entry in latest-releases.yaml without a YAML
// dependency. The file is a list of mappings with keys "file:" and "sha256:" (assumed layout,
// UNVERIFIED against the live file; the operator can bypass this with --alpine-url and
// --alpine-sha256).
func ParseLatestReleases(text, arch string) (Release, error) {
	var cur Release
	flush := func() (Release, bool) {
		if strings.HasPrefix(cur.File, "alpine-minirootfs-") && strings.HasSuffix(cur.File, "-"+arch+".tar.gz") && cur.SHA256 != "" {
			return cur, true
		}
		return Release{}, false
	}
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "- ") && strings.Contains(line, "-") && !strings.Contains(t, ":") {
			continue
		}
		if t == "-" || strings.HasPrefix(line, "-") {
			if r, ok := flush(); ok {
				return r, nil
			}
			cur = Release{}
			t = strings.TrimSpace(strings.TrimPrefix(t, "-"))
		}
		k, v, ok := strings.Cut(t, ":")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		switch strings.TrimSpace(k) {
		case "file":
			cur.File = v
		case "sha256":
			cur.SHA256 = v
		}
	}
	if r, ok := flush(); ok {
		return r, nil
	}
	return Release{}, errors.New("no alpine-minirootfs entry for " + arch + " in latest-releases.yaml")
}

// BranchOf extracts "v3.22" from "3.22.1" or "v3.22".
func BranchOf(version string) string {
	v := strings.TrimPrefix(version, "v")
	p := strings.Split(v, ".")
	if len(p) < 2 {
		return "v" + v
	}
	return "v" + p[0] + "." + p[1]
}

// SHA256Hex returns the hex digest of data (used by tests; the program hashes streams).
func SHA256Hex(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }

// ValidSHA256 reports whether s is 64 hex digits.
func ValidSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// ---- paths -----------------------------------------------------------------------------

// ValidMountBase refuses mount bases that could point at system paths. The final element must
// start with "jop" and the path must be absolute, clean and at least one level deep.
func ValidMountBase(p string) error {
	if p == "" || !strings.HasPrefix(p, "/") || path.Clean(p) != p || p == "/" {
		return fmt.Errorf("mount base %q must be a clean absolute path", p)
	}
	base := path.Base(p)
	if !strings.HasPrefix(base, "jop") {
		return fmt.Errorf("mount base %q: last element must start with jop", p)
	}
	if strings.ContainsAny(p, " \t\n\"'\\$") {
		return fmt.Errorf("mount base %q contains unsafe characters", p)
	}
	return nil
}

// Under reports whether p is strictly inside root (lexically).
func Under(root, p string) bool {
	root = path.Clean(root)
	p = path.Clean(p)
	return p != root && strings.HasPrefix(p, root+"/")
}

// ---- jail.conf -------------------------------------------------------------------------

// JailQuote quotes a jail.conf string value. UNVERIFIED: jail.conf(5) accepts backslash
// escapes for " and \ inside double quotes.
func JailQuote(s string) (string, error) {
	if strings.ContainsAny(s, "\x00\n\r") {
		return "", fmt.Errorf("jail.conf value contains a control character: %q", s)
	}
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, `$`, `\$`) // keep jail.conf variable expansion off
	return `"` + s + `"`, nil
}

// JailParams drives RenderJailConf.
type JailParams struct {
	Name         string
	Path         string
	Hostname     string
	VNet         bool // vnet jail; the epair is moved by `ifconfig X vnet <jail>` afterwards
	InheritIP    bool // bootstrap phase only: share the host stack so apk can reach the mirror
	DevfsRuleset int
}

// RenderJailConf renders a private jail.conf for one jail.
func RenderJailConf(p JailParams) (string, error) {
	if !strings.HasPrefix(p.Name, "jop") {
		return "", fmt.Errorf("jail name %q lacks the jop prefix", p.Name)
	}
	if p.VNet == p.InheritIP {
		return "", errors.New("exactly one of VNet and InheritIP must be set")
	}
	qp, err := JailQuote(p.Path)
	if err != nil {
		return "", err
	}
	qh, err := JailQuote(p.Hostname)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# JailOps prototype 6, private file, not /etc/jail.conf\n")
	fmt.Fprintf(&b, "%s {\n", p.Name)
	fmt.Fprintf(&b, "\tpath = %s;\n\thost.hostname = %s;\n\tpersist;\n", qp, qh)
	if p.DevfsRuleset > 0 {
		fmt.Fprintf(&b, "\tdevfs_ruleset = %d;\n", p.DevfsRuleset)
	}
	if p.VNet {
		b.WriteString("\tvnet;\n")
	} else {
		b.WriteString("\tip4 = inherit;\n\tip6 = inherit;\n")
	}
	b.WriteString("}\n")
	return b.String(), nil
}

// ---- devfs ----------------------------------------------------------------------------

// DevfsRules returns the argument lists for `devfs rule -s <n> ...`. The ruleset hides
// everything and unhides what a Linux userland needs. UNVERIFIED: the set of nodes is a
// reasoned minimum (null zero full random urandom tty ptmx pts fd std*), not a tested one.
func DevfsRules(n int) [][]string {
	s := strconv.Itoa(n)
	rules := [][]string{{"devfs", "rule", "-s", s, "add", "hide"}}
	for _, p := range []string{"null", "zero", "full", "random", "urandom", "tty", "ptmx", "pts", "pts/*", "fd", "fd/*", "stdin", "stdout", "stderr", "console"} {
		rules = append(rules, []string{"devfs", "rule", "-s", s, "add", "path", p, "unhide"})
	}
	return rules
}

// RulesetInUse parses `devfs rule showsets` (one set number per line; UNVERIFIED output format)
// and reports whether n is listed.
func RulesetInUse(showsets string, n int) bool {
	for _, f := range strings.Fields(showsets) {
		if v, err := strconv.Atoi(f); err == nil && v == n {
			return true
		}
	}
	return false
}

// ---- mounts ---------------------------------------------------------------------------

// Mount is one mount(8) invocation relative to the jail root.
type Mount struct {
	Type   string
	Source string
	Rel    string // below the jail root
	Opts   string
}

// MountList is the ordered mount set required for a Linux jail (design section 6). devfs comes
// first so fd and shm go on top of it.
func MountList(ruleset int) []Mount {
	return []Mount{
		{"devfs", "devfs", "dev", "ruleset=" + strconv.Itoa(ruleset)},
		{"fdescfs", "fdesc", "dev/fd", ""},
		// Verified on FreeBSD 15.1: the Linuxulator resolves paths through the emulation path
		// /compat/linux first, and as soon as that directory exists in the jail root a file create
		// below it fails with ENOENT (apk could not lock its database). Mirroring the root there
		// with nullfs makes creates land in the real tree, and /dev/shm is then provided by a tmpfs
		// mounted on the mirror's dev/shm (devfs refuses to create the directory itself; the
		// directory is created in the underlying filesystem through the mirror).
		{"nullfs", "{root}", "compat/linux", ""},
		{"tmpfs", "tmpfs", "compat/linux/dev/shm", "mode=1777"},
		{"linprocfs", "linprocfs", "proc", ""},
		{"linsysfs", "linsysfs", "sys", ""},
	}
}

// MountArgv builds the mount argv for m under root.
func MountArgv(root string, m Mount) []string {
	a := []string{"mount", "-t", m.Type}
	if m.Opts != "" {
		a = append(a, "-o", m.Opts)
	}
	src := strings.ReplaceAll(m.Source, "{root}", root)
	return append(a, src, path.Join(root, m.Rel))
}

// MountsUnder parses `mount -p` output (fstab format: dev mountpoint type opts ...) and returns
// the mount points strictly under root, deepest first. UNVERIFIED: `mount -p` field order.
func MountsUnder(mountP, root string) []string {
	var out []string
	for _, l := range strings.Split(mountP, "\n") {
		f := strings.Fields(l)
		if len(f) >= 3 && Under(root, f[1]) {
			out = append(out, f[1])
		}
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

// ---- file templates -------------------------------------------------------------------

// Resolv renders resolv.conf.
func Resolv(ns string) string { return "nameserver " + ns + "\n" }

// Repositories renders /etc/apk/repositories over plain http (apk verifies signatures).
func Repositories(branch string) string {
	return fmt.Sprintf("http://dl-cdn.alpinelinux.org/alpine/%s/main\nhttp://dl-cdn.alpinelinux.org/alpine/%s/community\n", branch, branch)
}

// NginxConf is a minimal self-contained config, started directly with -c.
func NginxConf() string {
	return `daemon on;
pid /run/jop-nginx.pid;
error_log /var/log/nginx/jop-error.log;
events { worker_connections 64; }
http {
	access_log /var/log/nginx/jop-access.log;
	server {
		listen 80;
		listen [::]:80;
		root /var/www/jop;
		location / { try_files $uri /index.html; }
	}
}
`
}

// NginxIndex is the page the check expects.
const NginxIndex = "jop6-ok\n"

// SshdConf is a minimal sshd config, started directly with -f.
func SshdConf() string {
	return `Port 22
PidFile /run/jop-sshd.pid
PermitRootLogin no
PasswordAuthentication no
KbdInteractiveAuthentication no
UsePAM no
HostKey /etc/ssh/ssh_host_ed25519_key
`
}

// Detach wraps a daemon command so that jexec does not wait for inherited pipes.
func Detach(cmd string) string { return cmd + " >/dev/null 2>&1 </dev/null" }

// ---- ifconfig parsing ------------------------------------------------------------------

// ParseAddrs extracts "inet a/prefixlen-or-mask" and "inet6 a" addresses from FreeBSD ifconfig
// output for one interface. Link-local fe80:: addresses are skipped because they are
// regenerated (UNVERIFIED: an interface moved into a vnet gets a new link-local address).
func ParseAddrs(ifconfigOut string) []string {
	var out []string
	for _, l := range strings.Split(ifconfigOut, "\n") {
		f := strings.Fields(l)
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "inet":
			out = append(out, "inet "+f[1])
		case "inet6":
			a := f[1]
			if i := strings.IndexByte(a, '%'); i >= 0 {
				a = a[:i]
			}
			if strings.HasPrefix(strings.ToLower(a), "fe80:") {
				continue
			}
			out = append(out, "inet6 "+a)
		}
	}
	sort.Strings(out)
	return out
}

// Missing returns the entries of want not present in have.
func Missing(want, have []string) []string {
	set := map[string]bool{}
	for _, h := range have {
		set[h] = true
	}
	var m []string
	for _, w := range want {
		if !set[w] {
			m = append(m, w)
		}
	}
	return m
}

// ---- failure table ---------------------------------------------------------------------

// Failure records one failed command exactly.
type Failure struct {
	Step    string
	Command string
	Code    int
	Output  string
}

// Failures accumulates failures; failing steps never abort independent later steps.
type Failures struct{ List []Failure }

// Add records a failure. Output is truncated to keep the verdict line pasteable.
func (f *Failures) Add(step, command string, code int, output string) {
	output = strings.TrimSpace(output)
	if len(output) > 400 {
		output = output[:400] + "...[truncated]"
	}
	f.List = append(f.List, Failure{step, command, code, output})
}

// Table renders the failures as a text table.
func (f *Failures) Table() string {
	if len(f.List) == 0 {
		return "no failed commands\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%-3s %-24s %-5s %s\n", "#", "STEP", "EXIT", "COMMAND -> OUTPUT")
	for i, x := range f.List {
		fmt.Fprintf(&b, "%-3d %-24s %-5d %s -> %s\n", i+1, x.Step, x.Code, x.Command, strings.ReplaceAll(x.Output, "\n", " | "))
	}
	return b.String()
}

// Summary is a one-line form for check details.
func (f *Failures) Summary(step string) string {
	var parts []string
	for _, x := range f.List {
		if x.Step == step {
			parts = append(parts, fmt.Sprintf("`%s` exit %d: %s", x.Command, x.Code, strings.ReplaceAll(x.Output, "\n", " | ")))
		}
	}
	return strings.Join(parts, "; ")
}

// BrandELFTree sets the ELF OS/ABI byte to 3 (Linux) on every executable or shared ELF file under
// root that carries the generic value 0. Verified on FreeBSD 15.1 arm64: busybox in the Alpine
// minirootfs is already branded Linux, but apk-tools and other binaries are not, and the kernel
// then refuses to execute them ("Exec format error", or a shell trying to read the ELF as a
// script). This is what brandelf -t Linux does, done in pure Go. It returns the number of files
// changed and the number of ELF files that were already branded.
func BrandELFTree(root string) (changed, already int, err error) {
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		f, oerr := os.OpenFile(p, os.O_RDWR, 0)
		if oerr != nil {
			return nil
		}
		defer f.Close()
		var h [8]byte
		if _, rerr := io.ReadFull(f, h[:]); rerr != nil {
			return nil
		}
		if h[0] != 0x7f || h[1] != 'E' || h[2] != 'L' || h[3] != 'F' {
			return nil
		}
		switch h[7] {
		case 3:
			already++
		case 0:
			if _, werr := f.WriteAt([]byte{3}, 7); werr == nil {
				changed++
			}
		}
		return nil
	})
	return changed, already, err
}
