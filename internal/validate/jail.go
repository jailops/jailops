package validate

import (
	"fmt"
	"net/netip"
	"path"
	"regexp"
	"strconv"
	"strings"

	"jailops.net/jailops/internal/schedule"
	"jailops.net/jailops/sdk/api"
)

// MaxAttachments is the number of network attachments a jail may have.
const MaxAttachments = 16

// JailSpec validates a jail name and spec.
func JailSpec(name string, s api.JailSpec) error {
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
	if s.Provision != nil {
		if err := provision(*s.Provision); err != nil {
			return err
		}
	}
	if s.Resources.CPU < 0 || s.Resources.CPU > 4096 {
		return Invalid("resources.cpu", "cpu must be between 0 and 4096")
	}
	if s.Resources.PIDs < 0 {
		return Invalid("resources.pids", "pids must not be negative")
	}
	for f, v := range map[string]string{"resources.memory": s.Resources.Memory, "resources.disk": s.Resources.Disk} {
		if v != "" {
			if _, err := ParseSize(v); err != nil {
				return Invalid(f, "%s %q is not a size such as 512M or 2G", f, v)
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
	if p := s.Firewall.Policy; p != "" {
		if err := Name("firewall.policy", p); err != nil {
			return err
		}
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
	seenHost := map[string]bool{}
	for i, m := range s.HostMounts {
		f := fmt.Sprintf("host_mounts[%d]", i)
		if err := AbsPath(f+".source", m.Source); err != nil {
			return err
		}
		if err := AbsPath(f+".target", m.Target); err != nil {
			return err
		}
		if m.Target == "/" {
			return Invalid(f+".target", "a host directory cannot be mounted on /")
		}
		if seenHost[m.Target] {
			return Invalid(f+".target", "duplicate mount point %q", m.Target)
		}
		seenHost[m.Target] = true
	}
	for i, v := range s.Volumes {
		f := fmt.Sprintf("volumes[%d]", i)
		if err := Name(f+".name", v.Name); err != nil {
			return err
		}
		if err := AbsPath(f+".mount", v.Mount); err != nil {
			return err
		}
		if v.Mount == "/" {
			return Invalid(f+".mount", "a volume cannot be mounted on /")
		}
		if v.Quota != "" {
			if _, err := ParseSize(v.Quota); err != nil {
				return Invalid(f+".quota", "quota %q is not a size", v.Quota)
			}
		}
		if seenName[v.Name] {
			return Invalid(f+".name", "duplicate volume %q", v.Name)
		}
		if seenMount[v.Mount] || seenHost[v.Mount] {
			return Invalid(f+".mount", "duplicate mount point %q", v.Mount)
		}
		seenName[v.Name], seenMount[v.Mount] = true, true
	}
	if s.Boot.Priority < -1000 || s.Boot.Priority > 1000 {
		return Invalid("boot.priority", "priority must be between -1000 and 1000")
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

var deviceRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)

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
		if !deviceRE.MatchString(name) {
			return Invalid(f+".name", "device name %q must match %s", name, deviceRE)
		}
		if seen[name] {
			return Invalid(f+".name", "duplicate network device %q", name)
		}
		seen[name] = true
		if n.MTU != 0 && (n.MTU < 576 || n.MTU > 9216) {
			return Invalid(f+".mtu", "mtu must be 0 or between 576 and 9216")
		}
		for fld, v := range map[string]string{"ingress": n.Ingress, "egress": n.Egress} {
			if v != "" {
				if _, err := ParseSize(v); err != nil {
					return Invalid(f+"."+fld, "%s %q is not a rate in bits per second such as 10M", fld, v)
				}
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
		if !deviceRE.MatchString(d.Name) {
			return Invalid(f+".name", "device name %q must match %s", d.Name, deviceRE)
		}
		if !allowed[d.Device] {
			return Invalid(f+".device", "%q is not on the devfs allow-list (%s)", d.Device, strings.Join(api.DevfsAllowList, ", "))
		}
		if dseen[d.Name] {
			return Invalid(f+".name", "duplicate devfs device %q", d.Name)
		}
		dseen[d.Name] = true
	}
	pseen := map[string]bool{}
	for i, p := range s.Proxy {
		f := fmt.Sprintf("proxy[%d]", i)
		if !deviceRE.MatchString(p.Name) {
			return Invalid(f+".name", "device name %q must match %s", p.Name, deviceRE)
		}
		if pseen[p.Name] {
			return Invalid(f+".name", "duplicate proxy device %q", p.Name)
		}
		pseen[p.Name] = true
		if _, _, err := ParseListen(p.Listen); err != nil {
			return Invalid(f+".listen", "%v", err)
		}
		if p.JailPort < 1 || p.JailPort > 65535 {
			return Invalid(f+".jail_port", "jail_port must be between 1 and 65535")
		}
		switch p.Proto {
		case "", "tcp", "udp":
		default:
			return Invalid(f+".proto", "proto %q must be tcp or udp", p.Proto)
		}
	}
	for i, p := range s.Profiles {
		if err := Name(fmt.Sprintf("profiles[%d]", i), p); err != nil {
			return err
		}
	}
	return nil
}

// ParseListen splits a proxy listener "host:port" or ":port" into the host
// address (empty for the uplink address) and the port.
func ParseListen(v string) (host string, port int, err error) {
	i := strings.LastIndex(v, ":")
	if i < 0 {
		return "", 0, fmt.Errorf("listen %q must be host:port or :port", v)
	}
	host = strings.Trim(v[:i], "[]")
	if host != "" {
		a, perr := netip.ParseAddr(host)
		if perr != nil || a.Zone() != "" || a.IsMulticast() || a.IsUnspecified() {
			return "", 0, fmt.Errorf("listen address %q is not an address of the host", host)
		}
		host = a.String()
	}
	n, perr := strconv.Atoi(v[i+1:])
	if perr != nil || n < 1 || n > 65535 {
		return "", 0, fmt.Errorf("listen port %q is not a port", v[i+1:])
	}
	return host, n, nil
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
