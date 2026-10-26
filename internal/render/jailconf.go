package render

import (
	"fmt"
	"sort"

	"jailops.net/jailops/internal/validate"
	"jailops.net/jailops/sdk/api"
)

// HelperCommand is the fixed privileged helper invoked by jail hooks. It
// receives only a phase and the jail name as jail.conf expands ${name}.
// UNVERIFIED: that jail.conf expands ${name} inside exec.* strings, and that
// jailops-priv grows a "hook" subcommand.
const HelperCommand = "/usr/local/libexec/jailops-priv hook"

// DefaultDevfsRuleset is devfsrules_jail from the stock FreeBSD devfs.rules.
const DefaultDevfsRuleset = 4

// AllowList is the set of allow.* parameters jail.conf may carry; the
// argument names omit the "allow." prefix.
var AllowList = map[string]bool{
	"set_hostname": true, "sysvipc": true, "raw_sockets": true, "chflags": true,
	"mlock": true, "reserved_ports": true, "read_msgbuf": true, "socket_af": true,
	"mount": true, "mount.devfs": true, "mount.fdescfs": true, "mount.nullfs": true,
	"mount.procfs": true, "mount.tmpfs": true, "mount.zfs": true,
}

// JailConfInput is everything the per-jail jail.conf depends on.
type JailConfInput struct {
	Name     string
	Path     string // absolute jail root
	Hostname string
	OS       api.OS
	// Interfaces are the jail-side interface names moved into the vnet.
	Interfaces []string
	// DevfsRuleset defaults to DefaultDevfsRuleset when zero.
	DevfsRuleset int
	// EnforceStatfs defaults to 2.
	EnforceStatfs *int
	// SecureLevel is omitted when nil.
	SecureLevel *int
	OSRelease   string // optional, e.g. "15.1-RELEASE"
	Allow       []string
	// FstabPath, when set, becomes mount.fstab.
	FstabPath string
}

// JailConf renders the jail.conf of one jail.
func JailConf(in JailConfInput) (string, error) {
	var b builder
	if err := validate.JailKey("name", in.Name); err != nil {
		return "", err
	}
	if err := validate.AbsPath("path", in.Path); err != nil {
		return "", err
	}
	host := in.Hostname
	if host == "" {
		host = in.Name
	}
	if err := validate.Hostname("hostname", host); err != nil {
		return "", err
	}
	if in.OS != api.OSFreeBSD && in.OS != api.OSLinux {
		return "", fmt.Errorf("render: unknown os %q", in.OS)
	}
	linux := in.OS == api.OSLinux
	rs := in.DevfsRuleset
	if rs == 0 {
		rs = DefaultDevfsRuleset
	}
	if rs < 1 || rs > 65535 {
		return "", fmt.Errorf("render: devfs ruleset %d out of range", rs)
	}
	es := 2
	if in.EnforceStatfs != nil {
		es = *in.EnforceStatfs
	}
	if es < 0 || es > 2 {
		return "", fmt.Errorf("render: enforce_statfs %d out of range", es)
	}
	if in.SecureLevel != nil && (*in.SecureLevel < -1 || *in.SecureLevel > 3) {
		return "", fmt.Errorf("render: securelevel %d out of range", *in.SecureLevel)
	}
	var ifs []string
	for _, i := range in.Interfaces {
		if err := validate.IfName("interface", i); err != nil {
			return "", err
		}
		q, _ := quoted("interface", i)
		ifs = append(ifs, q)
	}
	allow := append([]string(nil), in.Allow...)
	sort.Strings(allow)
	for i, a := range allow {
		if !AllowList[a] {
			return "", fmt.Errorf("render: allow.%s is not on the allow-list", a)
		}
		if i > 0 && allow[i-1] == a {
			return "", fmt.Errorf("render: duplicate allow.%s", a)
		}
	}
	path, err := quoted("path", in.Path)
	if err != nil {
		return "", err
	}
	hq, err := quoted("hostname", host)
	if err != nil {
		return "", err
	}

	b.line("%s", Header)
	b.line("%s {", in.Name)
	b.line("\tpath = %s;", path)
	b.line("\thost.hostname = %s;", hq)
	b.line("\tvnet;")
	if len(ifs) > 0 {
		b.line("\tvnet.interface = %s;", join(ifs, ", "))
	}
	if linux {
		// UNVERIFIED: Linux jails mount devfs from the fstab (Linuxulator
		// layout) and then need no mount.devfs; the ruleset below is only
		// enforced for devfs mounts made by jail(8) itself.
		b.line("\tdevfs_ruleset = %d;", rs)
	} else {
		b.line("\tdevfs_ruleset = %d;", rs)
		b.line("\tmount.devfs;")
	}
	if in.FstabPath != "" {
		if err := validate.AbsPath("fstab", in.FstabPath); err != nil {
			return "", err
		}
		q, _ := quoted("fstab", in.FstabPath)
		b.line("\tmount.fstab = %s;", q)
	}
	b.line("\texec.clean;")
	b.line("\texec.prestart = \"%s prestart ${name}\";", HelperCommand)
	b.line("\texec.poststart = \"%s poststart ${name}\";", HelperCommand)
	b.line("\texec.prestop = \"%s prestop ${name}\";", HelperCommand)
	b.line("\texec.poststop = \"%s poststop ${name}\";", HelperCommand)
	if !linux {
		b.line("\texec.start = \"/bin/sh /etc/rc\";")
		b.line("\texec.stop = \"/bin/sh /etc/rc.shutdown jail\";")
	}
	b.line("\tenforce_statfs = %d;", es)
	if in.SecureLevel != nil {
		b.line("\tsecurelevel = %d;", *in.SecureLevel)
	}
	if in.OSRelease != "" {
		if err := validate.OSRelease("osrelease", in.OSRelease); err != nil {
			return "", err
		}
		q, _ := quoted("osrelease", in.OSRelease)
		b.line("\tosrelease = %s;", q)
	}
	for _, a := range allow {
		b.line("\tallow.%s;", a)
	}
	if linux {
		// UNVERIFIED: a Linux jail has no FreeBSD init, so it must persist
		// without processes until JailOps removes it.
		b.line("\tpersist;")
	}
	b.line("}")
	return b.result()
}

func join(s []string, sep string) string {
	out := ""
	for i, x := range s {
		if i > 0 {
			out += sep
		}
		out += x
	}
	return out
}
