package validate

import (
	"fmt"
	"net/netip"
	"path"
	"regexp"
	"strings"

	"jailops.net/jailops/internal/schedule"
	"jailops.net/jailops/sdk/api"
)

// MaxAttachments is the number of network attachments a jail may have.
const MaxAttachments = 16

// JailSpec validates a jail name and spec.
func JailSpec(name string, s api.JailSpec) error {
	if err := api.CheckComment(s); err != nil {
		return err
	}
	if err := JailName(name); err != nil {
		return err
	}
	if _, err := ParseImageRef(s.Image); err != nil {
		return err
	}
	if s.Storage != "" {
		if err := Name("storage", s.Storage); err != nil {
			return err
		}
	}
	if s.Resources.DiskReservation != "" {
		if _, err := ParseSize(s.Resources.DiskReservation); err != nil {
			return Invalid("resources.disk_reservation", "disk_reservation %q is not a size", s.Resources.DiskReservation)
		}
	}
	switch s.Layout {
	case "", api.LayoutThin, api.LayoutThick:
	default:
		return Invalid("layout", "layout %q must be thin or thick", s.Layout)
	}
	if s.Hostname != "" {
		if err := Hostname("hostname", s.Hostname); err != nil {
			return err
		}
	}
	switch s.Update.Strategy {
	case "", api.UpdateRebase, api.UpdateInPlace, api.UpdateManual:
	default:
		return Invalid("update.strategy", "update strategy %q is unknown", s.Update.Strategy)
	}
	if err := updatePolicy(s.Update); err != nil {
		return err
	}
	if s.Shell != "" {
		if !strings.HasPrefix(s.Shell, "/") || len(s.Shell) > 255 || strings.ContainsAny(s.Shell, " \t\r\n\x00") || strings.Contains(s.Shell, "..") {
			return Invalid("shell", "shell %q must be an absolute path inside the jail, without spaces", s.Shell)
		}
	}
	if s.Prompt != "" {
		if err := api.CheckPrompt(s.Prompt); err != nil {
			return Invalid("prompt", "prompt: %v", err)
		}
	}
	if s.Provision != nil {
		if err := provision(*s.Provision); err != nil {
			return err
		}
	}
	if s.Resources.CPU < 0 || s.Resources.CPU > 4096 {
		return Invalid("resources.cpu", "the CPU limit must be between 0 and 4096 cores (--cpu)")
	}
	if s.Resources.PIDs < 0 {
		return Invalid("resources.pids", "the process limit must not be negative (--pids)")
	}
	if s.Resources.States < 0 || s.Resources.States > 10000000 {
		return Invalid("resources.states", "the state limit must be between 0 and 10000000")
	}
	for _, r := range []struct{ field, flag, v string }{{"resources.memory", "memory", s.Resources.Memory}, {"resources.disk", "disk", s.Resources.Disk}} {
		if r.v != "" {
			if _, err := ParseSize(r.v); err != nil {
				return Invalid(r.field, "%s %q is not a size such as 512M or 2G (--%s)", r.flag, r.v, r.flag)
			}
		}
	}
	if len(s.Network) > MaxAttachments {
		return Invalid("network", "at most %d network attachments", MaxAttachments)
	}
	for i, n := range s.Network {
		if err := attach(i, n); err != nil {
			return err
		}
	}
	if err := devices(s); err != nil {
		return err
	}
	if err := PolicyNames("firewall.policies", s.Firewall.Policies); err != nil {
		return err
	}
	seenTag := map[string]bool{}
	for i, t := range s.Firewall.Tags {
		f := fmt.Sprintf("firewall.tags[%d]", i)
		if err := Tag(f, t); err != nil {
			return err
		}
		if seenTag[t] {
			return Invalid(f, "duplicate tag %q", t)
		}
		seenTag[t] = true
	}
	seenName, seenMount := map[string]bool{}, map[string]bool{}
	for i, v := range s.Volumes {
		f := fmt.Sprintf("volumes[%d]", i)
		if err := Name(f+".volume", v.Name); err != nil {
			return err
		}
		if err := AbsPath(f+".mount", v.Mount); err != nil {
			return err
		}
		if v.Mount == "/" {
			return Invalid(f+".mount", "a volume cannot be mounted on /")
		}
		if seenName[v.Name] {
			return Invalid(f+".volume", "volume %q is attached twice", v.Name)
		}
		if seenMount[v.Mount] {
			return Invalid(f+".mount", "duplicate mount point %q", v.Mount)
		}
		seenName[v.Name], seenMount[v.Mount] = true, true
	}
	// nothing is mounted below a volume: a link in the first volume would carry the second mount elsewhere
	for i, a := range s.Volumes {
		for j, b := range s.Volumes {
			if i != j && strings.HasPrefix(b.Mount, strings.TrimSuffix(a.Mount, "/")+"/") {
				return Invalid(fmt.Sprintf("volumes[%d].mount", j), "mount point %q is inside the mount point %q of volume %q: nothing is mounted below a volume", b.Mount, a.Mount, a.Name)
			}
		}
	}
	if s.Boot.Priority < -1000 || s.Boot.Priority > 1000 {
		return Invalid("boot.priority", "priority must be between -1000 and 1000")
	}
	if s.Boot.StopTimeout < 0 || s.Boot.StopTimeout > api.MaxStopTimeout {
		return Invalid("boot.stop_timeout", "stop timeout must be between 0 and %d seconds", api.MaxStopTimeout)
	}
	for i, d := range s.Boot.Depends {
		f := fmt.Sprintf("boot.depends[%d]", i)
		if err := Name(f, d); err != nil {
			return err
		}
		if d == name {
			return Invalid(f, "a jail cannot depend on itself")
		}
	}
	for i, p := range s.Packages {
		if !pkgRE.MatchString(p) {
			return Invalid(fmt.Sprintf("packages[%d]", i), "package name %q is invalid", p)
		}
	}
	for k, v := range s.Env {
		if !envKeyRE.MatchString(k) {
			return Invalid("env", "environment name %q is invalid", k)
		}
		for i := 0; i < len(v); i++ {
			if v[i] == 0 {
				return Invalid("env."+k, "value contains a NUL byte")
			}
		}
	}
	return nil
}

// devices validates the named devices of a jail: unique names per kind, the
// devfs allow-list, proxy listeners and profile names.
func devices(s api.JailSpec) error {
	seen := map[string]bool{}
	for i, n := range s.Network {
		f := fmt.Sprintf("network[%d]", i)
		name := n.Name
		if name == "" {
			name = fmt.Sprintf("eth%d", i)
		}
		if !api.IsName(name, 31) {
			return Invalid(f+".name", "device name %q must follow the name rule (%s, at most 31 characters)", name, api.NamePattern)
		}
		if seen[name] {
			return Invalid(f+".name", "duplicate network device %q", name)
		}
		seen[name] = true
		if n.MTU != 0 && (n.MTU < 576 || n.MTU > 9216) {
			return Invalid(f+".mtu", "mtu must be 0 or between 576 and 9216")
		}
		if n.Egress != "" {
			if _, err := ParseRate(n.Egress); err != nil {
				return Invalid(f+".egress", "egress %q is not a rate in bits per second such as 10M", n.Egress)
			}
		}
		if n.Ingress != "" {
			if _, err := ParseRate(n.Ingress); err != nil {
				return Invalid(f+".ingress", "ingress %q is not a rate in bits per second such as 10M", n.Ingress)
			}
		}
	}
	allowed := map[string]bool{}
	for _, d := range api.DevfsAllowList {
		allowed[d] = true
	}
	dseen := map[string]bool{}
	for i, d := range s.Devfs {
		f := fmt.Sprintf("devfs[%d]", i)
		if !api.IsName(d.Name, 31) {
			return Invalid(f+".name", "device name %q must follow the name rule (%s, at most 31 characters)", d.Name, api.NamePattern)
		}
		if !allowed[d.Device] {
			return Invalid(f+".device", "%q is not on the devfs allow-list (%s)", d.Device, strings.Join(api.DevfsAllowList, ", "))
		}
		if dseen[d.Name] {
			return Invalid(f+".name", "duplicate devfs device %q", d.Name)
		}
		dseen[d.Name] = true
	}
	if len(s.Expose) > MaxExpose {
		return Invalid("expose", "a jail has at most %d exposed entries", MaxExpose)
	}
	eseen := map[string]bool{}
	for i, e := range s.Expose {
		f := fmt.Sprintf("expose[%d]", i)
		if err := ExposeEntry(f, e); err != nil {
			return err
		}
		for _, p := range e.Ports {
			k := e.Protocol() + "/" + p
			if eseen[k] {
				return Invalid(f+".ports", "%s %s is exposed twice", e.Protocol(), p)
			}
			eseen[k] = true
		}
	}
	for i, p := range s.Profiles {
		if err := Name(fmt.Sprintf("profiles[%d]", i), p); err != nil {
			return err
		}
	}
	return nil
}

// MaxExpose is the most entries a jail's exposure may have.
const MaxExpose = 64

// ExposeEntry checks one entry of a jail's exposure: the protocol, the ports, the families, the sources (an address, a
// prefix, an address set or any: the host, a jail and a tag never arrive from the Internet), the forward's host port and
// address, and the comment.
func ExposeEntry(f string, e api.ExposeEntry) error {
	if e.Proto != "" && e.Proto != "tcp" && e.Proto != "udp" {
		return Invalid(f+".proto", "proto %q must be tcp or udp", e.Proto)
	}
	if len(e.Ports) == 0 {
		return Invalid(f+".ports", "an exposure needs ports")
	}
	for _, p := range e.Ports {
		if _, _, err := ParsePortSpec(p); err != nil {
			return Invalid(f+".ports", "%q is not a port or a range of ports", p)
		}
	}
	fseen := map[string]bool{}
	for _, fam := range e.Families {
		if fam != "4" && fam != "6" {
			return Invalid(f+".families", "family %q must be 4 or 6", fam)
		}
		if fseen[fam] {
			return Invalid(f+".families", "family %s is named twice", fam)
		}
		fseen[fam] = true
	}
	for _, from := range e.From {
		k, _, err := ParseEndpoint(from)
		if err != nil {
			return Invalid(f+".from", "%s", err.Error())
		}
		if k != EndpointAny && k != EndpointSet && k != EndpointAddr {
			return Invalid(f+".from", "%q never arrives from the Internet: name an address, a prefix, an address set or any (a jail, a tag or the host is a firewall rule)", from)
		}
	}
	if e.HostPort != 0 {
		if e.HostPort < 1 || e.HostPort > 65535 {
			return Invalid(f+".host_port", "host_port must be between 1 and 65535")
		}
		if len(e.Ports) != 1 || strings.Contains(e.Ports[0], "-") {
			return Invalid(f+".host_port", "host_port applies to one port, not a range or a list")
		}
	}
	if e.Listen != "" {
		a, err := netip.ParseAddr(e.Listen)
		if err != nil || !a.Is4() || a.Zone() != "" || a.IsMulticast() || a.IsUnspecified() || a.IsLoopback() {
			return Invalid(f+".listen", "listen %q is not an IPv4 unicast address of the host", e.Listen)
		}
	}
	return Comment(f+".comment", e.Comment)
}

func attach(i int, n api.NetworkAttach) error {
	f := fmt.Sprintf("network[%d]", i)
	if err := Name(f+".network", n.Network); err != nil {
		return err
	}
	for _, c := range []struct {
		field, v string
		v4       bool
	}{{f + ".ipv4", n.IPv4, true}, {f + ".ipv6", n.IPv6, false}} {
		if c.v == "" || c.v == "auto" || c.v == "none" {
			continue
		}
		a, err := addr(c.field, c.v)
		if err != nil {
			return err
		}
		if a.Is4() != c.v4 {
			return Invalid(c.field, "%s %q has the wrong address family", c.field, c.v)
		}
		if a.IsUnspecified() || a.IsMulticast() || a.IsLoopback() {
			return Invalid(c.field, "%s %q is not a usable unicast address", c.field, c.v)
		}
	}
	if n.MAC != "" {
		return MAC(f+".mac", n.MAC)
	}
	return nil
}

// updatePolicy validates the update policy of design 8.4.
func updatePolicy(u api.UpdatePolicy) error {
	switch u.Mode {
	case "", api.UpdateModeManual:
	case api.UpdateModeNotify, api.UpdateModeManaged:
		if u.Schedule == "" {
			return Invalid("update.schedule", "update mode %s needs a schedule (a five-field cron expression)", u.Mode)
		}
		if u.Mode == api.UpdateModeManaged && !u.Packages && !u.OS {
			return Invalid("update.mode", "mode managed has nothing to apply: set updates.packages, updates.os or both")
		}
	default:
		return Invalid("update.mode", "update mode %q must be manual, notify or managed", u.Mode)
	}
	if u.Schedule != "" {
		if _, err := schedule.Parse(u.Schedule); err != nil {
			return Invalid("update.schedule", "update schedule %q: %v", u.Schedule, err)
		}
	}
	switch u.Reboot {
	case "", api.RebootManual, api.RebootAuto:
	default:
		return Invalid("update.reboot", "update reboot %q must be manual or auto", u.Reboot)
	}
	return nil
}

var (
	provPkgRE  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$`)
	provSvcRE  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	provUserRE = regexp.MustCompile(`^([a-z_][a-z0-9_-]{0,31}\$?|[0-9]{1,10})$`)
	provModeRE = regexp.MustCompile(`^0?[0-7]{3,4}$`)
)

// provision validates the provisioning declaration of design 8.5. Nothing in
// it is a shell string: packages, services and users are names, files are
// project-relative sources and absolute destinations, commands are argv.
func provision(p api.Provision) error {
	if len(p.Packages) > 200 || len(p.Files) > 200 || len(p.Commands) > 100 || len(p.Services.Enable) > 100 || len(p.Services.Start) > 100 {
		return Invalid("provision", "provision lists are too long")
	}
	for i, n := range p.Packages {
		if !provPkgRE.MatchString(n) {
			return Invalid(fmt.Sprintf("provision.packages[%d]", i), "package name %q is not valid", n)
		}
	}
	for i, f := range p.Files {
		fld := fmt.Sprintf("provision.files[%d]", i)
		if f.Source == "" || strings.HasPrefix(f.Source, "/") || strings.ContainsAny(f.Source, "\x00\\") || path.Clean(f.Source) != f.Source || f.Source == "." || strings.HasPrefix(f.Source, "../") {
			return Invalid(fld+".source", "source %q must be a clean path below the project's files, not an absolute or host path", f.Source)
		}
		if err := AbsPath(fld+".destination", f.Destination); err != nil {
			return err
		}
		if f.Destination == "/" || strings.Contains(f.Destination, "..") {
			return Invalid(fld+".destination", "destination %q is not usable", f.Destination)
		}
		for k, v := range map[string]string{"owner": f.Owner, "group": f.Group} {
			if v != "" && !provUserRE.MatchString(v) {
				return Invalid(fld+"."+k, "%s %q is not a user or group name or id", k, v)
			}
		}
		if f.Mode != "" && !provModeRE.MatchString(f.Mode) {
			return Invalid(fld+".mode", "mode %q must be octal such as 0644", f.Mode)
		}
	}
	for _, l := range [][]string{p.Services.Enable, p.Services.Start} {
		for i, n := range l {
			if !provSvcRE.MatchString(n) {
				return Invalid(fmt.Sprintf("provision.services[%d]", i), "service name %q is not valid", n)
			}
		}
	}
	for i, argv := range p.Commands {
		if len(argv) == 0 || argv[0] == "" || len(argv) > 64 {
			return Invalid(fmt.Sprintf("provision.commands[%d]", i), "a command is a non-empty argv of at most 64 words")
		}
		for _, a := range argv {
			if strings.ContainsRune(a, 0) || len(a) > 4096 {
				return Invalid(fmt.Sprintf("provision.commands[%d]", i), "a command word is not valid")
			}
		}
	}
	return nil
}

// LayoutFits refuses the thin layout for a Linux jail: the persistent datasets of a thin jail are ZFS mounts that the
// nullfs mirror of the root at compat/linux does not show, so a Linux process would see empty directories. It is asked
// when a jail is made, with the operating system of its image.
func LayoutFits(s api.JailSpec) error {
	if s.Layout == api.LayoutThin && s.OS == api.OSLinux {
		return Invalid("layout", "a Linux jail is thick: the thin layout is for FreeBSD jails (leave --layout out)")
	}
	return nil
}

// UpdateStrategyFits refuses the strategy inplace for a thin FreeBSD jail: a thin jail moves to a newer generation of
// its image (rebase, the default), and the update that would change it in place is not made for it. It is asked when a
// jail is made, or when its strategy is changed, with the layout and the operating system that are settled by then; a
// jail that was stored with the strategy before the rule keeps it and stays updatable, so it is not part of JailSpec.
func UpdateStrategyFits(s api.JailSpec) error {
	if s.Update.Strategy == api.UpdateInPlace && s.Layout != api.LayoutThick && s.OS != api.OSLinux {
		return Invalid("update.strategy", "update strategy inplace is for thick and Linux jails; a thin jail moves to a newer generation of its image (rebase, the default)")
	}
	return nil
}
