// Package thinupgrade holds the pure logic of prototype 2 (thin-jail upgrade experiment,
// design section 4). Everything here is unit-tested on Linux. Nothing in this package was
// checked against real FreeBSD output; parsers of FreeBSD tool output are written from
// memory of the tools and marked UNVERIFIED: where the format is assumed.
package thinupgrade

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Persist is one persistent dataset of a thin jail: a dataset under the jail container
// mounted at Mount inside the jail root.
type Persist struct {
	Name  string // dataset leaf under <root>/jail
	Mount string // absolute path inside the jail, e.g. /usr/local
}

// DefaultVarPaths are the /var subpaths of design section 4.2.
var DefaultVarPaths = []string{"db", "log", "mail", "spool"}

// PersistentPaths builds the persistent list: /etc, /usr/local, selected /var paths, /home, /root.
func PersistentPaths(varPaths []string) []Persist {
	l := []Persist{{"etc", "/etc"}, {"usr-local", "/usr/local"}}
	for _, v := range varPaths {
		v = strings.Trim(strings.TrimSpace(v), "/")
		if v == "" {
			continue
		}
		l = append(l, Persist{"var-" + strings.ReplaceAll(v, "/", "-"), "/var/" + v})
	}
	return append(l, Persist{"home", "/home"}, Persist{"rhome", "/root"})
}

// MountList renders the path list the way the prototype reports it.
func MountList(ps []Persist) string {
	var m []string
	for _, p := range ps {
		m = append(m, p.Mount)
	}
	return strings.Join(m, " ")
}

// ParseWritten parses `zfs get -H -p -o value written[@snap]` output.
func ParseWritten(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "-" {
		return 0, fmt.Errorf("no value %q", s)
	}
	return strconv.ParseInt(s, 10, 64)
}

// HumanBytes formats a size.
func HumanBytes(n int64) string {
	const u = "KMGT"
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	f, i := float64(n)/1024, 0
	for f >= 1024 && i < len(u)-1 {
		f /= 1024
		i++
	}
	return fmt.Sprintf("%.1f %ciB", f, u[i])
}

// Version is a parsed FreeBSD release or pkg version.
type Version struct {
	Major, Minor int
	Raw          string
}

var verRE = regexp.MustCompile(`^(\d+)\.(\d+)`)

// ParseVersion accepts "15.0-RELEASE-p2", "15.1-STABLE", "15.0", "15.0p1".
func ParseVersion(s string) (Version, error) {
	s = strings.TrimSpace(s)
	m := verRE.FindStringSubmatch(s)
	if m == nil {
		return Version{}, fmt.Errorf("unparsable version %q", s)
	}
	a, _ := strconv.Atoi(m[1])
	b, _ := strconv.Atoi(m[2])
	return Version{a, b, s}, nil
}

// Less orders by major then minor.
func (v Version) Less(o Version) bool {
	return v.Major < o.Major || v.Major == o.Major && v.Minor < o.Minor
}

// MajorChange reports a major-version change and the note to print.
func MajorChange(from, to Version) (bool, string) {
	if from.Major == to.Major {
		return false, fmt.Sprintf("same major (%d): minor/patch update via pkg upgrade is the expected path", from.Major)
	}
	return true, fmt.Sprintf("MAJOR change %d -> %d: the pkg ABI changes, every package in /usr/local must be rebuilt "+
		"or reinstalled (pkg upgrade -f), persistent /etc needs a full etcupdate with a new baseline, and /var/db/pkg "+
		"must be migrated. The design must treat this as a separate, explicit strategy, not as `rebase`", from.Major, to.Major)
}

// KernelTooOld implements preflight 2 of design 4.3: refuse if the image is newer than the host kernel.
func KernelTooOld(kernel, image Version) bool { return kernel.Less(image) }

// Merge3Result is the outcome of a keyed three-way merge.
type Merge3Result struct {
	Lines     []string
	Conflicts []string // keys
}

// PasswdKey keys master.passwd lines by user name.
func PasswdKey(line string) string {
	if i := strings.IndexByte(line, ':'); i >= 0 && !strings.HasPrefix(line, "#") {
		return line[:i]
	}
	return "\x00" + line
}

// RCKey keys rc.conf lines by variable name; comments and blanks key as themselves.
func RCKey(line string) string {
	t := strings.TrimSpace(line)
	if strings.HasPrefix(t, "#") || !strings.Contains(t, "=") {
		return "\x00" + line
	}
	return strings.TrimSpace(t[:strings.IndexByte(t, '=')])
}

// Merge3Keyed merges ours and theirs against base line-by-key. It never guesses: a key changed
// differently on both sides is a conflict (ours is kept in Lines). This is the model of what
// JailOps wants for master.passwd and rc.conf; the prototype compares it with etcupdate.
func Merge3Keyed(base, ours, theirs []string, key func(string) string) Merge3Result {
	idx := func(l []string) (map[string]string, []string) {
		m := map[string]string{}
		var order []string
		for _, x := range l {
			k := key(x)
			if _, ok := m[k]; !ok {
				order = append(order, k)
				m[k] = x
			}
		}
		return m, order
	}
	bm, bo := idx(base)
	om, oo := idx(ours)
	tm, to := idx(theirs)
	var keys []string
	seen := map[string]bool{}
	for _, l := range [][]string{oo, to, bo} {
		for _, k := range l {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	var r Merge3Result
	for _, k := range keys {
		b, bok := bm[k]
		o, ook := om[k]
		t, tok := tm[k]
		pick := func(v string, ok bool) {
			if ok {
				r.Lines = append(r.Lines, v)
			}
		}
		switch {
		case ook == tok && o == t:
			pick(o, ook)
		case ook == bok && o == b:
			pick(t, tok)
		case tok == bok && t == b:
			pick(o, ook)
		default:
			r.Conflicts = append(r.Conflicts, strings.TrimPrefix(k, "\x00"))
			pick(o, ook)
		}
	}
	return r
}

// HasConflictMarkers detects diff3/merge conflict markers left in a file.
func HasConflictMarkers(content string) bool {
	var a, b bool
	for _, l := range strings.Split(content, "\n") {
		if strings.HasPrefix(l, "<<<<<<<") {
			a = true
		}
		if a && strings.HasPrefix(l, ">>>>>>>") {
			b = true
		}
	}
	return a && b
}

var conflLine = regexp.MustCompile(`^\s*C\s+(/\S+)\s*$`)

// ParseEtcupdateStatus extracts conflicting paths from `etcupdate status` output.
// UNVERIFIED: the output format is assumed. Two forms are accepted: a header line containing
// "conflict" followed by indented absolute paths, and lines "C /path". The prototype also
// scans for conflict markers, so a wrong guess here is caught there.
func ParseEtcupdateStatus(out string) []string {
	var res []string
	in := false
	seen := map[string]bool{}
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			res = append(res, p)
		}
	}
	for _, l := range strings.Split(out, "\n") {
		if m := conflLine.FindStringSubmatch(l); m != nil {
			add(m[1])
			continue
		}
		t := strings.TrimSpace(l)
		switch {
		case t == "":
			in = false
		case strings.HasSuffix(t, ":") && !strings.HasPrefix(t, "/"):
			in = strings.Contains(strings.ToLower(t), "conflict")
		case in && strings.HasPrefix(t, "/"):
			add(t)
		}
	}
	sort.Strings(res)
	return res
}

// Drift is the /var skeleton difference between two generations restricted to persistent mounts.
type Drift struct {
	Added   []string // directories new in g2 inside a persistent mount (hidden by the old data)
	Removed []string // directories gone in g2 inside a persistent mount (stale in the jail)
}

func under(p string, mounts []string) bool {
	for _, m := range mounts {
		if p == m || strings.HasPrefix(p, m+"/") {
			return true
		}
	}
	return false
}

// DriftDirs compares two directory listings (absolute paths inside the image) and reports
// differences inside persistent mounts. Directories exactly equal to a mount are ignored.
func DriftDirs(oldDirs, newDirs, mounts []string) Drift {
	o, n := map[string]bool{}, map[string]bool{}
	for _, d := range oldDirs {
		o[path.Clean(d)] = true
	}
	for _, d := range newDirs {
		n[path.Clean(d)] = true
	}
	isMount := func(p string) bool {
		for _, m := range mounts {
			if p == m {
				return true
			}
		}
		return false
	}
	var d Drift
	for p := range n {
		if !o[p] && under(p, mounts) && !isMount(p) {
			d.Added = append(d.Added, p)
		}
	}
	for p := range o {
		if !n[p] && under(p, mounts) && !isMount(p) {
			d.Removed = append(d.Removed, p)
		}
	}
	sort.Strings(d.Added)
	sort.Strings(d.Removed)
	return d
}

// RepoConf renders a pkg repository file. UNVERIFIED: the FreeBSD 15 base repo URL, the
// srv mirror type and the fingerprint directory follow the documented pkgbase setup.
func RepoConf(name, url string, fingerprints bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: {\n  url: %q,\n", name, url)
	if strings.HasPrefix(url, "pkg+") {
		b.WriteString("  mirror_type: \"srv\",\n")
	}
	if fingerprints {
		// Verified on FreeBSD 15.1: the base repository is signed with the keys in
		// /usr/share/keys/pkgbase-<major>, not /usr/share/keys/pkg.
		dir := "/usr/share/keys/pkg"
		if strings.HasPrefix(name, "FreeBSD-base") {
			dir = "/usr/share/keys/pkgbase-${VERSION_MAJOR}"
		}
		fmt.Fprintf(&b, "  signature_type: \"fingerprints\",\n  fingerprints: %q,\n", dir)
	} else {
		b.WriteString("  signature_type: \"none\",\n")
	}
	b.WriteString("  enabled: yes\n}\n")
	return b.String()
}

// JailConf renders a jail.conf for one persistent, network-less jail.
func JailConf(name, root string) string {
	return fmt.Sprintf(`%[1]s {
  path = %[2]q;
  host.hostname = %[1]q;
  persist;
  mount.devfs;
  devfs_ruleset = 4;
  ip4 = disable;
  ip6 = disable;
  allow.raw_sockets = 0;
  exec.clean;
}
`, name, root)
}

// PkgArgs builds the argv of a `pkg -r root` call with an isolated repository directory.
func PkgArgs(root, reposDir, abi string, args ...string) []string {
	a := []string{"pkg", "-r", root, "-o", "REPOS_DIR=" + reposDir, "-o", "ASSUME_ALWAYS_YES=yes", "-o", "IGNORE_OSVERSION=yes"}
	if abi != "" {
		a = append(a, "-o", "ABI="+abi)
	}
	return append(a, args...)
}

// Finding classifies persistence after a rollback: which of the probe paths still hold data.
type Finding struct {
	Path     string
	Survived bool
}

// SurvivalSummary returns the paths that survived and those that did not.
func SurvivalSummary(fs []Finding) (ok, lost []string) {
	for _, f := range fs {
		if f.Survived {
			ok = append(ok, f.Path)
		} else {
			lost = append(lost, f.Path)
		}
	}
	return
}
