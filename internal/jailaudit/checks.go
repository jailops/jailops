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
	// Running jails have their mounts and listening sockets read.
	Running bool
	// Hostname and Addresses are what the JailOps block of /etc/hosts holds; Shell and Prompt what the jail's spec
	// says of root's shell.
	Hostname  string
	Addresses []string
	Shell     string
	Prompt    string
	// Listeners are the listening sockets of a running jail (sockstat); SocketsErr says why they were not read.
	Listeners  []Listener
	SocketsErr string
	// Exposed are the jail's exposed ports, "tcp/80", ranges "tcp/8000-8100".
	Exposed []string
	// JailopsRepoURL is the url of the host's JailOps package repository, which a jail with an app has ("" for none).
	JailopsRepoURL string
}

// Listener is one listening socket of a running jail.
type Listener struct {
	Command  string
	Proto    string
	Port     int
	Loopback bool
}

// The groups of the checks.
const (
	GroupSecurity = "security"
	GroupPractice = "practice"
)

// Evaluate runs every check on in, the security checks first. Only a security check is ever critical; a practice check
// warns at most.
func Evaluate(in Input) []api.JailAuditCheck {
	f := in.Facts
	rc := shellVars(f.Texts["/etc/rc.conf"], f.Texts["/etc/rc.conf.local"])
	pc := shellVars(f.Texts["/etc/periodic.conf"], f.Texts["/etc/periodic.conf.local"])
	return []api.JailAuditCheck{
		checkRootLinks(f),
		checkUID0(f),
		checkPasswords(f),
		checkSetID(in),
		checkRepos(in),
		checkBaseRepo(f),
		checkChecksums(in),
		checkSSHD(f),
		checkWorldKernel(pc),
		checkPeriodicOutput(pc),
		checkCronJitter(rc),
		checkRcServices(rc),
		checkShell(in),
		checkHosts(in),
		checkVolumes(in),
		checkDiskLimit(in),
		checkSize(in),
		checkListeners(in),
	}
}

func pass(id, group, detail string) api.JailAuditCheck {
	return api.JailAuditCheck{ID: id, Group: group, Result: api.AuditPass, Detail: detail}
}

func found(id, group string, r api.JailAuditResult, detail, fix string) api.JailAuditCheck {
	return api.JailAuditCheck{ID: id, Group: group, Result: r, Detail: detail, Fix: fix}
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

func checkUID0(f Facts) api.JailAuditCheck {
	const id = "accounts.uid0"
	switch {
	case !f.Accounts.Read:
		return found(id, GroupSecurity, api.AuditWarn, "/etc/master.passwd cannot be read", "look at /etc/master.passwd of the jail")
	case len(f.Accounts.UID0) > 0:
		return found(id, GroupSecurity, api.AuditCritical, "accounts with uid 0 besides root and toor: "+listed(f.Accounts.UID0, 0),
			"remove each account (pw userdel NAME in the jail) unless you made it on purpose, and find out how it got there")
	}
	return pass(id, GroupSecurity, "root and toor are the only accounts with uid 0")
}

func checkPasswords(f Facts) api.JailAuditCheck {
	const id = "accounts.passwords"
	if !f.Accounts.Read {
		return found(id, GroupSecurity, api.AuditWarn, "/etc/master.passwd cannot be read", "look at /etc/master.passwd of the jail")
	}
	var bad []string
	switch f.Accounts.Root {
	case PassEmpty:
		bad = append(bad, "root has an empty password (a member of wheel becomes root with su without one; jails made before root was locked in new images have it: pw usermod root -w no in the jail)")
	case PassSet:
		bad = append(bad, "root has a password")
	}
	if f.Accounts.Toor == PassEmpty || f.Accounts.Toor == PassSet {
		bad = append(bad, "toor is not locked")
	}
	if len(f.Accounts.Empty) > 0 {
		bad = append(bad, "accounts with an empty password: "+listed(f.Accounts.Empty, 0))
	}
	if len(bad) == 0 {
		return pass(id, GroupSecurity, "root is locked and no account has an empty password")
	}
	return found(id, GroupSecurity, api.AuditWarn, strings.Join(bad, "; "),
		"lock the password in the jail (pw usermod root -w no; pw lock NAME for another account): jexec and jailops shell need none")
}

func checkSetID(in Input) api.JailAuditCheck {
	const id = "files.setid"
	f := in.Facts
	if !in.HasPkgDB {
		return found(id, GroupSecurity, api.AuditInfo, "the jail has no package database: nothing tells the base's set-id files from others", "")
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
		"look at each file; remove it or clear the bit (chmod u-s,g-s FILE in the jail) unless you put it there on purpose")
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

func checkBaseRepo(f Facts) api.JailAuditCheck {
	const id = "pkg.base_repository"
	for _, r := range repos(f.Texts) {
		if r.Name == "FreeBSD-base" && r.Enabled {
			return found(id, GroupSecurity, api.AuditWarn, "FreeBSD-base is enabled inside the jail (by "+r.where()+"): pkg there takes base packages it cannot install (FreeBSD bug 293817) and the base is upgraded from the host",
				`echo 'FreeBSD-base: { enabled: no }' > /usr/local/etc/pkg/repos/FreeBSD-base.conf in the jail; jailops jail upgrade and jailops jail base add handle the base`)
		}
	}
	return pass(id, GroupSecurity, "FreeBSD-base is disabled inside the jail; the host upgrades the base")
}

func checkChecksums(in Input) api.JailAuditCheck {
	const id = "base.checksums"
	f := in.Facts
	if !in.HasPkgDB || f.Verified == 0 && len(f.Modified) == 0 {
		return found(id, GroupSecurity, api.AuditInfo, "no checksums of base programs to compare (the jail has no base packages in its package database)", "")
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

func checkSSHD(f Facts) api.JailAuditCheck {
	const id = "sshd.config"
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
		"log in with keys: set PermitRootLogin prohibit-password (or no), PasswordAuthentication no and KbdInteractiveAuthentication no, then service sshd reload in the jail")
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

func checkWorldKernel(pc map[string]string) api.JailAuditCheck {
	const id = "periodic.world_kernel"
	if v, ok := pc["daily_status_world_kernel"]; ok && !isYes(v) {
		return pass(id, GroupPractice, `daily_status_world_kernel="NO": the daily report does not compare the userland with the host's kernel`)
	}
	return found(id, GroupPractice, api.AuditInfo, "the daily report compares the userland with the kernel, which in a jail is the host's: it reports a mismatch whenever the host is on another patch level",
		`sysrc -f /etc/periodic.conf daily_status_world_kernel="NO" in the jail`)
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
		`sysrc -f /etc/periodic.conf daily_output="/var/log/daily.log" (weekly.log, monthly.log, and /var/log/security for the security reports); newsyslog rotates them`)
}

func checkCronJitter(rc map[string]string) api.JailAuditCheck {
	const id = "cron.jitter"
	if f := strings.Fields(rc["cron_flags"]); slices.ContainsFunc(f, func(s string) bool { return strings.HasPrefix(s, "-J") }) {
		return pass(id, GroupPractice, "cron starts the jobs of root (periodic among them) at a random second: cron_flags=\""+rc["cron_flags"]+"\"")
	}
	return found(id, GroupPractice, api.AuditInfo, "cron starts the jobs of root, periodic among them, at the same second in every jail of the host",
		`sysrc cron_flags="-J 60" and service cron restart in the jail`)
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
		"sysrc -x NAME in the jail for each; keep sendmail_enable only if the jail receives mail")
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
	return found(id, GroupPractice, api.AuditWarn, "mounts in the jail without nosuid (a set-uid file there runs as its owner): "+listed(bad, 0),
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

// exposed says whether a port of proto is among the jail's exposed ports.
func exposed(list []string, proto string, port int) bool {
	for _, e := range list {
		p, ports, ok := strings.Cut(e, "/")
		if !ok || p != proto {
			continue
		}
		lo, hi, isRange := strings.Cut(ports, "-")
		a, err1 := strconv.Atoi(lo)
		b := a
		var err2 error
		if isRange {
			b, err2 = strconv.Atoi(hi)
		}
		if err1 == nil && err2 == nil && port >= a && port <= b {
			return true
		}
	}
	return false
}

func checkListeners(in Input) api.JailAuditCheck {
	const id = "ports.listening"
	switch {
	case !in.Running:
		return found(id, GroupPractice, api.AuditInfo, "the jail is not running: its listening sockets are read while it runs", "")
	case in.SocketsErr != "":
		return found(id, GroupPractice, api.AuditInfo, "the listening sockets were not read: "+in.SocketsErr, "")
	}
	seen := map[string]bool{}
	var odd []string
	for _, l := range in.Listeners {
		k := fmt.Sprintf("%s/%d", l.Proto, l.Port)
		if l.Loopback || seen[k] || exposed(in.Exposed, l.Proto, l.Port) {
			continue
		}
		seen[k] = true
		odd = append(odd, k+" ("+l.Command+")")
	}
	if len(odd) == 0 {
		return pass(id, GroupPractice, "every port the jail listens on beyond loopback is exposed")
	}
	sort.Strings(odd)
	return found(id, GroupPractice, api.AuditInfo, "listening beyond loopback on ports the jail does not expose: "+listed(odd, 0),
		"jailops expose "+in.Jail+" PORT if the outside is meant to reach it; otherwise bind the service to 127.0.0.1 or leave it")
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
