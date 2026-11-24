package jailaudit

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"jailops.net/jailops/sdk/api"
)

// Input is everything the checks look at: the Facts of the root and what the daemon knows of the jail.
type Input struct {
	// Jail is the jail's name, as the fixes name it.
	Jail  string
	Facts Facts
	// HasPkgDB is false for a root without a package database: nothing tells the base's files from others.
	HasPkgDB bool
	// Owned are the files the installed packages own (base and ports): a set-id file among them is a package's.
	Owned map[string]bool
	// Packages and BasePackages count the installed packages; UsedBytes is the space the root takes.
	Packages, BasePackages int
	UsedBytes              int64
	// DiskLimit is the jail's resources.disk ("" for none).
	DiskLimit string
	// Running jails have their mounts read.
	Running bool
	// Hostname and Addresses are what the JailOps block of /etc/hosts holds; Shell and Prompt what the jail's spec
	// says of root's shell.
	Hostname  string
	Addresses []string
	Shell     string
	Prompt    string
	// JailopsRepoURL is the url of the host's JailOps package repository, which a jail with an app has ("" for none).
	JailopsRepoURL string
	// Vulns is what pkg audit found in a copy of the jail's package database with the host's vulnerability database.
	Vulns VulnAudit
	// BaseUpdates is what the last update check of the jail said of its base packages.
	BaseUpdates BaseUpdates
	// Params are the jail parameters of the jail.conf JailOps renders for the jail (ParseJailConf).
	Params JailParams
	// DevfsOwn is the devfs ruleset JailOps makes for the jail's devices (0 when the jail names none: the stock
	// DevfsJail).
	DevfsOwn int
}

// VulnAudit is the outcome of pkg audit on a copy of the jail's package database.
type VulnAudit struct {
	// NotRun says why pkg audit did not run ("" when it ran).
	NotRun string
	// DBDays is the age of the host's vulnerability database in whole days.
	DBDays int
	// Packages are the vulnerable packages pkg audit named.
	Packages []VulnPackage
}

// VulnPackage is one package pkg audit names as vulnerable: its name, version and the ids of the advisories (CVE, or
// the VuXML id of an entry without one).
type VulnPackage struct {
	Name, Version string
	IDs           []string
}

// BaseUpdates is what the last update check of the jail (jailops update check) said of its base packages: the
// audit fetches no catalogue itself.
type BaseUpdates struct {
	// Checked is false when no update check of the jail ran since the daemon started.
	Checked bool
	At      time.Time
	// Pending names the newer base packages ("" for none); Err says why the check did not look at the base.
	Pending, Err string
}

// The bounds of the checks.
const (
	// VulnDBMaxDays is the age of the host's vulnerability database after which the audit asks for a fresh one.
	VulnDBMaxDays = 7
	// MaxAdvisories is the number of advisory ids named per vulnerable package.
	MaxAdvisories = 5
	// DevfsJail is the stock ruleset of jails (devfsrules_jail of /etc/defaults/devfs.rules).
	DevfsJail = 4
)

// The groups of the checks.
const (
	GroupSecurity = "security"
	GroupPractice = "practice"
)

// Evaluate runs the checks on in. The default checks say what the jail shows and what FreeBSD's documentation names
// as practice (the Handbook's chapters on jails and on security, the CIS FreeBSD Benchmark): each is PASS, WARN or
// CRITICAL, or INFO when it could not be made. With suggest the suggestions follow: notes on how a jail runs better
// on JailOps, PASS or INFO only and marked Suggestion, which never count as a finding.
func Evaluate(in Input, suggest bool) []api.JailAuditCheck {
	f := in.Facts
	out := []api.JailAuditCheck{
		checkRootLinks(f),
		checkUID0(in),
		checkPasswords(in),
		checkSetID(in),
		checkStickyBit(in),
		checkWorldFiles(in),
		checkUnowned(in),
		checkRepos(in),
		checkBaseRepo(in),
		checkKmodsRepo(in),
		checkVulns(in),
		checkBaseUpdates(in),
		checkChecksums(in),
		checkSSHD(in),
		checkEnforceStatfs(in),
	}
	if !suggest {
		return out
	}
	rc := shellVars(f.Texts["/etc/rc.conf"], f.Texts["/etc/rc.conf.local"])
	pc := shellVars(f.Texts["/etc/periodic.conf"], f.Texts["/etc/periodic.conf.local"])
	for _, c := range []api.JailAuditCheck{
		checkRootLocked(in),
		checkWorldKernel(pc),
		checkPeriodicOutput(pc),
		checkCronJitter(in, rc),
		checkRcServices(rc),
		checkShell(in),
		checkHosts(in),
		checkVolumes(in),
		checkDiskLimit(in),
		checkSize(in),
		checkSecureLevel(in),
		checkAllow(in),
		checkDevfsRuleset(in),
	} {
		c.Group, c.Suggestion = GroupPractice, true
		out = append(out, c)
	}
	return out
}

func pass(id, group, detail string) api.JailAuditCheck {
	return api.JailAuditCheck{ID: id, Group: group, Result: api.AuditPass, Detail: detail}
}

func found(id, group string, r api.JailAuditResult, detail, fix string) api.JailAuditCheck {
	return api.JailAuditCheck{ID: id, Group: group, Result: r, Detail: detail, Fix: fix}
}

// notChecked is a default check that could not be made: INFO, and why.
func notChecked(id, why, fix string) api.JailAuditCheck {
	return found(id, GroupSecurity, api.AuditInfo, "not checked: "+why, fix)
}

// inJail is a command run in the jail, as a fix names it.
func inJail(jail, cmd string) string {
	return "jailops exec " + jail + " -- " + cmd
}

// listed joins at most MaxListed names and says how many more there are.
func listed(names []string, more int) string {
	if len(names) > MaxListed {
		more += len(names) - MaxListed
		names = names[:MaxListed]
	}
	s := strings.Join(names, ", ")
	if more > 0 {
		s += fmt.Sprintf(" and %d more", more)
	}
	return s
}

func checkRootLinks(f Facts) api.JailAuditCheck {
	const id = "root.links"
	if len(f.Suspected) == 0 {
		return pass(id, GroupSecurity, "no link out of the root on a path the base's packages own, pkg's own trees hold no link, no pkg.conf turns plugins on")
	}
	return found(id, GroupSecurity, api.AuditCritical, strings.Join(f.Suspected, "; "),
		"this is what an attack on the host's pkg plants: look at the jail first; remove each link or make it point inside the jail, and remove the plugin settings")
}

func checkUID0(in Input) api.JailAuditCheck {
	const id = "accounts.uid0"
	f := in.Facts
	switch {
	case !f.Accounts.Read:
		return found(id, GroupSecurity, api.AuditWarn, "/etc/master.passwd cannot be read", "look at /etc/master.passwd of the jail")
	case len(f.Accounts.UID0) > 0:
		return found(id, GroupSecurity, api.AuditCritical, "accounts with uid 0 besides root and toor: "+listed(f.Accounts.UID0, 0),
			"remove each account ("+inJail(in.Jail, "pw userdel NAME")+") unless you made it on purpose, and find out how it got there")
	}
	return pass(id, GroupSecurity, "root and toor are the only accounts with uid 0")
}

func checkPasswords(in Input) api.JailAuditCheck {
	const id = "accounts.passwords"
	f := in.Facts
	if !f.Accounts.Read {
		return found(id, GroupSecurity, api.AuditWarn, "/etc/master.passwd cannot be read", "look at /etc/master.passwd of the jail")
	}
	var empty []string
	for _, a := range []struct{ name, state string }{{"root", f.Accounts.Root}, {"toor", f.Accounts.Toor}} {
		if a.state == PassEmpty {
			empty = append(empty, a.name)
		}
	}
	empty = append(empty, f.Accounts.Empty...)
	if len(empty) == 0 {
		return pass(id, GroupSecurity, "no account has an empty password")
	}
	detail := "accounts with an empty password: " + listed(empty, 0)
	if f.Accounts.Root == PassEmpty {
		detail += " (a member of wheel becomes root with su without one)"
	}
	return found(id, GroupSecurity, api.AuditWarn, detail,
		"lock each one: "+inJail(in.Jail, "pw lock NAME")+", and for root "+inJail(in.Jail, "pw usermod root -w no")+" (jexec and jailops shell need no password)")
}

// checkRootLocked is a suggestion: root and toor locked, as the images of JailOps make them.
func checkRootLocked(in Input) api.JailAuditCheck {
	const id = "accounts.root_locked"
	f := in.Facts
	if !f.Accounts.Read {
		return found(id, GroupPractice, api.AuditInfo, "/etc/master.passwd cannot be read", "")
	}
	var set []string
	for _, a := range []struct{ name, state string }{{"root", f.Accounts.Root}, {"toor", f.Accounts.Toor}} {
		if a.state == PassSet {
			set = append(set, a.name)
		}
	}
	if len(set) == 0 {
		return pass(id, GroupPractice, "root and toor take no password")
	}
	return found(id, GroupPractice, api.AuditInfo, strings.Join(set, " and ")+" take a password: jexec and jailops shell need none, and the images of JailOps lock root",
		inJail(in.Jail, "pw usermod root -w no")+" (pw lock toor for toor)")
}

func checkSetID(in Input) api.JailAuditCheck {
	const id = "files.setid"
	f := in.Facts
	if !in.HasPkgDB {
		return notChecked(id, "the jail has no package database: nothing tells the base's set-id files from others", "")
	}
	var odd []string
	for _, p := range f.SetID {
		if !in.Owned[p] {
			odd = append(odd, p)
		}
	}
	cut := ""
	if f.SetIDCut {
		cut = fmt.Sprintf(" (the walk stopped after %d entries)", MaxWalk)
	}
	if len(odd) == 0 && f.SetIDMore == 0 {
		return pass(id, GroupSecurity, "every set-uid and set-gid file belongs to an installed package"+cut)
	}
	return found(id, GroupSecurity, api.AuditWarn, "set-uid or set-gid files that no package installed: "+listed(odd, f.SetIDMore)+cut,
		"look at each file; remove it or clear the bit ("+inJail(in.Jail, "chmod u-s,g-s FILE")+") unless you put it there on purpose")
}

// repo is one package repository as pkg sees it after reading every file: where it is and whether it is on.
type repo struct {
	// File is the file that gave the url, On the last one that said whether it is enabled.
	Name, URL, File, On string
	Enabled             bool
}

// where is the file that decides whether the repository is on: the last one to say so, else the one that made it.
func (r repo) where() string {
	if r.On != "" {
		return r.On
	}
	return r.File
}

// repos merges the repository files of the jail in pkg's order: a later file overrides a key of an earlier one, and a
// repository is enabled unless a file says no.
func repos(texts map[string]string) []repo {
	m := map[string]*repo{}
	var order []string
	for _, dir := range RepoDirs {
		var files []string
		for p := range texts {
			if path.Dir(p) == dir && strings.HasSuffix(p, ".conf") {
				files = append(files, p)
			}
		}
		sort.Strings(files)
		for _, p := range files {
			for _, b := range uclBlocks(texts[p]) {
				r := m[b.name]
				if r == nil {
					r = &repo{Name: b.name, Enabled: true, File: p}
					m[b.name] = r
					order = append(order, b.name)
				}
				if v, ok := b.keys["url"]; ok {
					r.URL, r.File = v, p
				}
				if v, ok := b.keys["enabled"]; ok {
					r.Enabled, r.On = isYes(v), p
				}
			}
		}
	}
	out := make([]repo, 0, len(order))
	for _, n := range order {
		out = append(out, *m[n])
	}
	return out
}

type uclBlock struct {
	name string
	keys map[string]string
}

var (
	reUCLStart = regexp.MustCompile(`^\s*"?([A-Za-z0-9_.-]+)"?\s*[:=]?\s*\{`)
	reUCLKey   = regexp.MustCompile(`(?i)^\s*(url|enabled)\s*[:=]\s*(?:"([^"]*)"|([^,;}\s]*))`)
)

// uclBlocks reads the top-level blocks of a repository file of pkg (UCL as the files of FreeBSD write it: NAME: { key:
// value, ... }), with the keys url and enabled. Comments (#, //) are skipped.
func uclBlocks(text string) []uclBlock {
	var out []uclBlock
	depth := 0
	var cur *uclBlock
	for _, l := range strings.Split(text, "\n") {
		if i := strings.Index(l, "#"); i >= 0 {
			l = l[:i]
		}
		if i := strings.Index(l, "//"); i >= 0 && (i == 0 || l[i-1] != ':') {
			l = l[:i]
		}
		if depth == 0 {
			if m := reUCLStart.FindStringSubmatch(l); m != nil {
				out = append(out, uclBlock{name: m[1], keys: map[string]string{}})
				cur = &out[len(out)-1]
				l = l[strings.Index(l, "{")+1:]
				depth = 1
			}
		}
		if depth == 1 && cur != nil {
			for _, part := range strings.Split(l, ",") {
				if m := reUCLKey.FindStringSubmatch(part); m != nil {
					cur.keys[strings.ToLower(m[1])] = m[2] + m[3]
				}
			}
		}
		if depth > 0 {
			depth += strings.Count(l, "{") - strings.Count(l, "}")
			if depth <= 0 {
				depth, cur = 0, nil
			}
		}
	}
	return out
}

func isYes(v string) bool {
	switch strings.ToLower(strings.Trim(v, `"' `)) {
	case "yes", "true", "on", "1":
		return true
	}
	return false
}

// officialRepos are the repositories FreeBSD's own files name.
var officialRepos = []string{"FreeBSD", "FreeBSD-ports", "FreeBSD-ports-kmods", "FreeBSD-base"}

// freebsdHost says whether a repository url is on FreeBSD's package servers.
func freebsdHost(raw string) bool {
	u, err := url.Parse(strings.TrimPrefix(raw, "pkg+"))
	if err != nil {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return h == "pkg.freebsd.org" || strings.HasSuffix(h, ".freebsd.org")
}

func checkRepos(in Input) api.JailAuditCheck {
	const id = "pkg.repositories"
	var odd []string
	for _, r := range repos(in.Facts.Texts) {
		if !r.Enabled {
			continue
		}
		switch {
		case slices.Contains(officialRepos, r.Name) && (r.URL == "" || freebsdHost(r.URL)):
		case r.Name == "jailops" && in.JailopsRepoURL != "" && r.URL == in.JailopsRepoURL:
		default:
			u := r.URL
			if u == "" {
				u = "no url"
			}
			odd = append(odd, fmt.Sprintf("%s (%s, in %s)", r.Name, u, r.File))
		}
	}
	if len(odd) == 0 {
		return pass(id, GroupSecurity, "the enabled package repositories are FreeBSD's (and the host's JailOps repository)")
	}
	return found(id, GroupSecurity, api.AuditWarn, "repositories that are not FreeBSD's: "+listed(odd, 0),
		"keep a repository you added on purpose; otherwise remove its file or set enabled: no in it")
}

func checkBaseRepo(in Input) api.JailAuditCheck {
	const id = "pkg.base_repository"
	for _, r := range repos(in.Facts.Texts) {
		if r.Name == "FreeBSD-base" && r.Enabled {
			return found(id, GroupSecurity, api.AuditWarn, "FreeBSD-base is enabled inside the jail (by "+r.where()+"): pkg there takes base packages it cannot install (FreeBSD bug 293817) and the base is upgraded from the host",
				inJail(in.Jail, `sh -c 'echo "FreeBSD-base: { enabled: no }" > /usr/local/etc/pkg/repos/FreeBSD-base.conf'`)+"; jailops jail upgrade and jailops jail base add handle the base")
		}
	}
	return pass(id, GroupSecurity, "FreeBSD-base is disabled inside the jail; the host upgrades the base")
}

func checkKmodsRepo(in Input) api.JailAuditCheck {
	const id = "pkg.kmods_repository"
	for _, r := range repos(in.Facts.Texts) {
		if r.Name == "FreeBSD-ports-kmods" && r.Enabled {
			return found(id, GroupSecurity, api.AuditWarn, "FreeBSD-ports-kmods is enabled inside the jail (by "+r.where()+"): kernel modules belong to the host's kernel, so the repository is of no use here and costs a catalogue fetch at every pkg update",
				inJail(in.Jail, `sh -c 'echo "FreeBSD-ports-kmods: { enabled: no }" > /usr/local/etc/pkg/repos/FreeBSD-ports-kmods.conf'`))
		}
	}
	return pass(id, GroupSecurity, "FreeBSD-ports-kmods is disabled inside the jail")
}

func checkChecksums(in Input) api.JailAuditCheck {
	const id = "base.checksums"
	f := in.Facts
	if !in.HasPkgDB || f.Verified == 0 && len(f.Modified) == 0 {
		return notChecked(id, "no checksums of base programs to compare (the jail has no base packages in its package database)", "")
	}
	if len(f.Modified) == 0 {
		msg := fmt.Sprintf("%d programs and libraries of the base match their packages", f.Verified)
		if f.NotCompared > 0 {
			msg += fmt.Sprintf(" (%d more not compared: the audit reads at most %d GiB)", f.NotCompared, MaxHashTotal>>30)
		}
		return pass(id, GroupSecurity, msg)
	}
	return found(id, GroupSecurity, api.AuditWarn, "programs or libraries of the base differ from their packages: "+listed(f.Modified, f.ModifiedMore),
		"find out who changed them first; the next jailops jail upgrade "+in.Jail+" that has a newer base package puts its files back, and jailops jail rollback "+in.Jail+" (stopped) returns to a snapshot")
}

// sshdValues reads the keywords of sshd_config the checks need, the first value of each as sshd takes it, up to the
// first Match block.
func sshdValues(text string) map[string]string {
	out := map[string]string{}
	for _, l := range strings.Split(text, "\n") {
		f := strings.Fields(strings.ReplaceAll(l, "=", " "))
		if len(f) < 2 || strings.HasPrefix(f[0], "#") {
			continue
		}
		k := strings.ToLower(f[0])
		if k == "match" {
			break
		}
		if _, ok := out[k]; !ok {
			out[k] = strings.ToLower(f[1])
		}
	}
	return out
}

func checkSSHD(in Input) api.JailAuditCheck {
	const id = "sshd.config"
	f := in.Facts
	if !f.SSHD {
		return pass(id, GroupSecurity, "sshd is not installed")
	}
	v := sshdValues(f.Texts["/etc/ssh/sshd_config"])
	var bad []string
	if v["permitrootlogin"] == "yes" {
		bad = append(bad, "PermitRootLogin yes")
	}
	if v["passwordauthentication"] == "yes" {
		bad = append(bad, "PasswordAuthentication yes")
	}
	// keyboard-interactive through PAM takes a password: FreeBSD's sshd has UsePAM and KbdInteractiveAuthentication
	// yes unless the file says otherwise (sshd -T of the stock file)
	kbd, ok := v["kbdinteractiveauthentication"]
	if !ok {
		kbd = v["challengeresponseauthentication"]
	}
	if v["usepam"] != "no" && kbd != "no" {
		bad = append(bad, "KbdInteractiveAuthentication through PAM (a password; on by default)")
	}
	if len(bad) == 0 {
		return pass(id, GroupSecurity, "sshd is installed; /etc/ssh/sshd_config allows neither root logins nor passwords")
	}
	return found(id, GroupSecurity, api.AuditWarn, "/etc/ssh/sshd_config has "+strings.Join(bad, " and "),
		"log in with keys: the lines PermitRootLogin prohibit-password (or no), PasswordAuthentication no and KbdInteractiveAuthentication no in /etc/ssh/sshd_config of the jail, then "+inJail(in.Jail, "service sshd reload"))
}

// shellVars reads name="value" lines of rc.conf-style files, later files and lines winning. Anything that is not a
// plain assignment is skipped.
var reShellVar = regexp.MustCompile(`^\s*([A-Za-z_][A-Za-z0-9_]*)=(?:"([^"]*)"|'([^']*)'|([^\s#;]*))`)

func shellVars(texts ...string) map[string]string {
	out := map[string]string{}
	for _, t := range texts {
		for _, l := range strings.Split(t, "\n") {
			if m := reShellVar.FindStringSubmatch(l); m != nil {
				out[m[1]] = m[2] + m[3] + m[4]
			}
		}
	}
	return out
}

// walkCut says the walk of the root stopped at its bound.
func walkCut(f Facts) string {
	if f.SetIDCut {
		return fmt.Sprintf(" (the walk stopped after %d entries)", MaxWalk)
	}
	return ""
}

func checkStickyBit(in Input) api.JailAuditCheck {
	const id = "files.sticky_bit"
	f := in.Facts
	if len(f.WorldDirs) == 0 && f.WorldDirsMore == 0 {
		return pass(id, GroupSecurity, "every directory anyone may write to has the sticky bit"+walkCut(f))
	}
	return found(id, GroupSecurity, api.AuditWarn, "directories anyone may write to without the sticky bit (anyone may remove or rename the files of others there): "+listed(f.WorldDirs, f.WorldDirsMore)+walkCut(f),
		"set the sticky bit ("+inJail(in.Jail, "chmod +t DIR")+") or take the write permission of others away (chmod o-w DIR)")
}

func checkWorldFiles(in Input) api.JailAuditCheck {
	const id = "files.world_writable"
	f := in.Facts
	if len(f.WorldFiles) == 0 && f.WorldFilesMore == 0 {
		return pass(id, GroupSecurity, "no file outside /tmp and /var/tmp may be written by anyone"+walkCut(f))
	}
	return found(id, GroupSecurity, api.AuditWarn, "files anyone may write to: "+listed(f.WorldFiles, f.WorldFilesMore)+walkCut(f),
		"take the write permission of others away ("+inJail(in.Jail, "chmod o-w FILE")+") unless the file is meant to be shared that way")
}

func checkUnowned(in Input) api.JailAuditCheck {
	const id = "files.unowned"
	f := in.Facts
	if !f.OwnersRead {
		return notChecked(id, "/etc/master.passwd or /etc/group of the jail cannot be read", "")
	}
	if len(f.Unowned) == 0 && f.UnownedMore == 0 {
		return pass(id, GroupSecurity, "every file and directory belongs to an account and a group of the jail"+walkCut(f))
	}
	return found(id, GroupSecurity, api.AuditWarn, "files and directories whose owner or group the jail does not have (an account made later with that id owns them): "+listed(f.Unowned, f.UnownedMore)+walkCut(f),
		"give each one an owner of the jail ("+inJail(in.Jail, "chown USER:GROUP PATH")+") or remove it")
}

func checkVulns(in Input) api.JailAuditCheck {
	const id = "pkg.vulnerabilities"
	v := in.Vulns
	refresh := "pkg audit -F on the host (the daily periodic run of the host does it when security_status_pkgaudit_enable is on, pkg's default)"
	switch {
	case !in.HasPkgDB:
		return notChecked(id, "the jail has no package database", "")
	case v.NotRun != "":
		return notChecked(id, v.NotRun, refresh)
	}
	stale := ""
	if v.DBDays > VulnDBMaxDays {
		stale = fmt.Sprintf(" (the vulnerability database of the host is %d days old: it is not current)", v.DBDays)
	}
	if len(v.Packages) == 0 {
		if stale != "" {
			return found(id, GroupSecurity, api.AuditInfo, "pkg audit names no vulnerable package"+stale, refresh)
		}
		return pass(id, GroupSecurity, "pkg audit names no vulnerable package")
	}
	var names []string
	for _, p := range v.Packages {
		ids := p.IDs
		more := ""
		if len(ids) > MaxAdvisories {
			more = fmt.Sprintf(" and %d more", len(ids)-MaxAdvisories)
			ids = ids[:MaxAdvisories]
		}
		s := p.Name + "-" + p.Version
		if len(ids) > 0 {
			s += " (" + strings.Join(ids, ", ") + more + ")"
		}
		names = append(names, s)
	}
	fix := "upgrade the packages: " + inJail(in.Jail, "pkg upgrade") + " for the ports packages, jailops jail upgrade " + in.Jail + " for the base (FreeBSD-*); pkg audit in the jail describes each advisory"
	if stale != "" {
		fix += "; and " + refresh
	}
	return found(id, GroupSecurity, api.AuditWarn, "packages with known vulnerabilities (pkg-audit(8)): "+listed(names, 0)+stale, fix)
}

func checkBaseUpdates(in Input) api.JailAuditCheck {
	const id = "base.updates"
	b := in.BaseUpdates
	when := b.At.UTC().Format("2006-01-02 15:04 UTC")
	switch {
	case !b.Checked:
		return notChecked(id, "no update check of the jail ran since the daemon started; the audit fetches no catalogue",
			"jailops update check "+in.Jail+", or jailops jail upgrade "+in.Jail+" --dry-run")
	case b.Pending != "":
		return found(id, GroupSecurity, api.AuditWarn, "the update check of "+when+" found newer base packages: "+b.Pending,
			"jailops jail upgrade "+in.Jail+" (--dry-run shows the change first)")
	case b.Err != "":
		return notChecked(id, "the update check of "+when+" did not look at the base: "+b.Err, "jailops jail upgrade "+in.Jail+" --dry-run")
	}
	return pass(id, GroupSecurity, "the update check of "+when+" found no newer base packages")
}

func checkEnforceStatfs(in Input) api.JailAuditCheck {
	const id = "jail.enforce_statfs"
	p := in.Params
	switch {
	case !p.Read:
		return notChecked(id, "the jail.conf of the jail cannot be read: "+p.Err, "")
	case p.EnforceStatfs < 2:
		why := ""
		if slices.Contains(p.Allow, "mount.zfs") {
			why = " (zfs_delegation needs it: the jail mounts its own dataset)"
		}
		return found(id, GroupSecurity, api.AuditWarn, fmt.Sprintf("enforce_statfs = %d: the jail sees mount points beyond its root%s", p.EnforceStatfs, why),
			"turn zfs_delegation off unless the jail needs its own dataset (jailops config set "+in.Jail+" zfs_delegation false)")
	}
	return pass(id, GroupSecurity, "enforce_statfs = 2: the jail sees only the mount points below its root")
}

// The suggestions: what a jail on JailOps runs better with. Each is PASS or INFO.

func checkWorldKernel(pc map[string]string) api.JailAuditCheck {
	const id = "periodic.world_kernel"
	if v, ok := pc["daily_status_world_kernel"]; ok && !isYes(v) {
		return pass(id, GroupPractice, `daily_status_world_kernel="NO": the daily report does not compare the userland with the host's kernel`)
	}
	return found(id, GroupPractice, api.AuditInfo, "the daily report compares the userland with the kernel, which in a jail is the host's: it reports a mismatch whenever the host is on another patch level",
		`the line daily_status_world_kernel="NO" in /etc/periodic.conf of the jail`)
}

func checkPeriodicOutput(pc map[string]string) api.JailAuditCheck {
	const id = "periodic.output"
	var mail []string
	for _, k := range []string{"daily_output", "weekly_output", "monthly_output", "daily_status_security_output"} {
		if v := pc[k]; !strings.HasPrefix(v, "/") {
			mail = append(mail, k)
		}
	}
	if len(mail) == 0 {
		return pass(id, GroupPractice, "the reports of periodic go to files in /var/log")
	}
	return found(id, GroupPractice, api.AuditInfo, "periodic mails its reports to root, in a jail where nobody reads that mail: "+strings.Join(mail, ", "),
		`the lines daily_output="/var/log/daily.log", weekly_output="/var/log/weekly.log", monthly_output="/var/log/monthly.log" and daily_status_security_output="/var/log/security" in /etc/periodic.conf of the jail; newsyslog rotates them`)
}

func checkCronJitter(in Input, rc map[string]string) api.JailAuditCheck {
	const id = "cron.jitter"
	if f := strings.Fields(rc["cron_flags"]); slices.ContainsFunc(f, func(s string) bool { return strings.HasPrefix(s, "-J") }) {
		return pass(id, GroupPractice, "cron starts the jobs of root (periodic among them) at a random second: cron_flags=\""+rc["cron_flags"]+"\"")
	}
	return found(id, GroupPractice, api.AuditInfo, "cron starts the jobs of root, periodic among them, at the same second in every jail of the host",
		`the line cron_flags="-J 60" in /etc/rc.conf of the jail, then `+inJail(in.Jail, "service cron restart"))
}

// nojailVars are the rc.conf knobs of scripts that rc(8) skips in a jail (their KEYWORD line has nojail, read from
// /etc/rc.d of FreeBSD 15), or that name a kernel facility a jail does not have: setting them does nothing.
var nojailVars = []string{"accounting_enable", "auditd_enable", "autofs_enable", "devd_enable", "devmatch_enable", "dumpdev",
	"hostid_enable", "kld_list", "kldxref_enable", "linux_enable", "moused_enable", "nfs_client_enable", "ntpd_enable",
	"ntpdate_enable", "powerd_enable", "quota_enable", "rctl_enable", "savecore_enable", "sysvipc_enable", "watchdogd_enable"}

func checkRcServices(rc map[string]string) api.JailAuditCheck {
	const id = "rc.conf"
	var noop []string
	for _, k := range nojailVars {
		v, ok := rc[k]
		if !ok {
			continue
		}
		if (strings.HasSuffix(k, "_enable") && isYes(v)) || (!strings.HasSuffix(k, "_enable") && v != "" && !strings.EqualFold(v, "no")) {
			noop = append(noop, k+"=\""+v+"\"")
		}
	}
	mta := strings.EqualFold(rc["sendmail_enable"], "yes")
	if len(noop) == 0 && !mta {
		return pass(id, GroupPractice, "rc.conf sets nothing that only the host's kernel can do, and no mail server")
	}
	var parts []string
	if len(noop) > 0 {
		parts = append(parts, "rc.conf sets what does nothing in a jail (rc skips it, or the kernel is the host's): "+strings.Join(noop, ", "))
	}
	if mta {
		parts = append(parts, `sendmail_enable="YES" runs a mail server in the jail`)
	}
	return found(id, GroupPractice, api.AuditInfo, strings.Join(parts, "; "),
		"remove each line from /etc/rc.conf (or /etc/rc.conf.local) of the jail; keep sendmail_enable only if the jail receives mail")
}

func checkShell(in Input) api.JailAuditCheck {
	const id = "shell.prompt"
	f := in.Facts
	want := in.Shell
	if want == "" {
		want = api.ShPath
	}
	sh := f.Accounts.RootShell
	switch {
	case !f.Accounts.Read:
		return found(id, GroupPractice, api.AuditInfo, "the shell of root cannot be read", "")
	case sh != want:
		return found(id, GroupPractice, api.AuditInfo, "root's shell is "+sh+", the jail's spec says "+want,
			"jailops jail shell-setup "+in.Jail+" sets the shell of the spec; or set the spec's shell to "+sh)
	case want == api.ShPath && in.Prompt != api.PromptNone && !f.ShrcPrompt:
		return found(id, GroupPractice, api.AuditInfo, "root's shell is sh, and /root/.shrc does not read the JailOps prompt",
			"jailops jail shell-setup "+in.Jail+" writes the block at the end of /root/.shrc")
	}
	return pass(id, GroupPractice, "root's shell is "+sh+" with the JailOps prompt")
}

func checkHosts(in Input) api.JailAuditCheck {
	const id = "hosts.block"
	h, ok := in.Facts.Texts["/etc/hosts"]
	if len(in.Addresses) == 0 || in.Hostname == "" {
		return pass(id, GroupPractice, "the jail has no address: /etc/hosts has no JailOps block to hold")
	}
	if !ok {
		why := "/etc/hosts is missing"
		if r := in.Facts.Unread["/etc/hosts"]; r != "" {
			why = "/etc/hosts is not read: " + r
		}
		return found(id, GroupPractice, api.AuditInfo, why+", so the jail's own name does not resolve to its addresses", "make /etc/hosts a plain file; JailOps writes its block at the next start")
	}
	var block []string
	in2 := false
	for _, l := range strings.Split(h, "\n") {
		switch {
		case strings.HasPrefix(l, "# BEGIN JailOps"):
			in2 = true
		case strings.HasPrefix(l, "# END JailOps"):
			in2 = false
		case in2:
			block = append(block, l)
		}
	}
	text := strings.Join(block, "\n")
	for _, a := range in.Addresses {
		if !strings.Contains(text, a+"\t"+in.Hostname) {
			return found(id, GroupPractice, api.AuditInfo, "the JailOps block of /etc/hosts does not name "+a+" as "+in.Hostname+": the jail's own name may not resolve (sudo and many daemons then wait for DNS)",
				"JailOps writes the block when the jail starts: restart the jail when convenient, or add the line yourself")
		}
	}
	return pass(id, GroupPractice, "/etc/hosts resolves "+in.Hostname+" to the jail's addresses")
}

func checkVolumes(in Input) api.JailAuditCheck {
	const id = "volumes.nosuid"
	if !in.Running {
		return pass(id, GroupPractice, "the jail is not running, nothing is mounted in it; JailOps mounts volumes nosuid")
	}
	var bad []string
	for _, m := range in.Facts.Mounts {
		if !m.NoSUID {
			bad = append(bad, m.Path)
		}
	}
	if len(bad) == 0 {
		return pass(id, GroupPractice, fmt.Sprintf("%d volume and host directory mounts, all nosuid", len(in.Facts.Mounts)))
	}
	return found(id, GroupPractice, api.AuditInfo, "mounts in the jail without nosuid (a set-uid file there runs as its owner): "+listed(bad, 0),
		"JailOps mounts its volumes nosuid; a mount without it was made by hand or by the jail's own fstab: remount it with -o nosuid")
}

func checkDiskLimit(in Input) api.JailAuditCheck {
	const id = "disk.limit"
	if in.DiskLimit != "" {
		return pass(id, GroupPractice, "the jail has a disk limit of "+in.DiskLimit)
	}
	return found(id, GroupPractice, api.AuditInfo, "the jail has no disk limit: it can fill its storage", "jailops config set "+in.Jail+" resources.disk=10G (a size that fits the jail)")
}

func checkSize(in Input) api.JailAuditCheck {
	const id = "disk.size"
	return found(id, GroupPractice, api.AuditInfo, fmt.Sprintf("the root takes %s with %d packages (%d of the base); a jail of the minimal set (FreeBSD-set-minimal-jail) has about 50 packages in about 53 MB",
		mib(in.UsedBytes), in.Packages, in.BasePackages), "")
}

func mib(n int64) string {
	return strconv.FormatFloat(float64(n)/(1<<20), 'f', 1, 64) + " MiB"
}

func checkSecureLevel(in Input) api.JailAuditCheck {
	const id = "jail.securelevel"
	p := in.Params
	switch {
	case !p.Read:
		return found(id, GroupPractice, api.AuditInfo, "the jail.conf of the jail cannot be read: "+p.Err, "")
	case p.SecureLevel != nil && *p.SecureLevel >= 2:
		return pass(id, GroupPractice, fmt.Sprintf("securelevel = %d", *p.SecureLevel))
	}
	lvl := "jail.conf sets no securelevel: the jail runs at the host's kern.securelevel (-1 unless the host raises it)"
	if p.SecureLevel != nil {
		lvl = fmt.Sprintf("securelevel = %d", *p.SecureLevel)
	}
	return found(id, GroupPractice, api.AuditInfo, lvl+"; 2 is the hardened setting (security(7)): no file flag can be cleared, no disk written raw, and the time set only forward",
		"raise it when nothing in the jail needs to change what securelevel 2 freezes (docs/security-model.md)")
}

func checkAllow(in Input) api.JailAuditCheck {
	const id = "jail.allow"
	p := in.Params
	switch {
	case !p.Read:
		return found(id, GroupPractice, api.AuditInfo, "the jail.conf of the jail cannot be read: "+p.Err, "")
	case len(p.Allow) == 0:
		return pass(id, GroupPractice, "jail.conf sets no allow.* parameter")
	}
	var names []string
	for _, a := range p.Allow {
		names = append(names, "allow."+a)
	}
	return found(id, GroupPractice, api.AuditInfo, "jail.conf sets "+strings.Join(names, ", ")+": each one gives the jail something the host's kernel otherwise keeps from it",
		"keep each one the jail needs; jailops config "+in.Jail+" shows the keys that set them")
}

func checkDevfsRuleset(in Input) api.JailAuditCheck {
	const id = "jail.devfs_ruleset"
	p := in.Params
	switch {
	case !p.Read:
		return found(id, GroupPractice, api.AuditInfo, "the jail.conf of the jail cannot be read: "+p.Err, "")
	case p.DevfsRuleset == DevfsJail:
		return pass(id, GroupPractice, "devfs_ruleset = 4: the stock ruleset of jails")
	case p.DevfsRuleset != 0 && p.DevfsRuleset == in.DevfsOwn:
		return pass(id, GroupPractice, fmt.Sprintf("devfs_ruleset = %d: the ruleset JailOps makes for the devices of the jail's spec", p.DevfsRuleset))
	}
	return found(id, GroupPractice, api.AuditInfo, fmt.Sprintf("devfs_ruleset = %d: neither the stock ruleset of jails (4) nor the one JailOps makes for the jail's devices", p.DevfsRuleset),
		"restart the jail: JailOps renders its jail.conf from the spec")
}

// RepoURL is the url the repository file text gives the repository name ("" for none).
func RepoURL(text, name string) string {
	for _, b := range uclBlocks(text) {
		if b.name == name {
			return b.keys["url"]
		}
	}
	return ""
}
