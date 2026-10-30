package api

import (
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Host management (appliance mode, docs/appliance.md). JailOps manages parts of the host itself only for the modules
// the administrator lists in jailopsd.yaml (host.manage.modules) and in the helper's priv.conf (host-manage); with
// neither, nothing changes on the host and the routes below only read. Module "identity" is the host name, the
// resolvers and NTP (extension host_identity): GET /api/v1/host/identity reads them, PUT changes them,
// GET /api/v1/host/snapshots lists the copies taken before each change and
// POST /api/v1/host/snapshots/{name}/restore puts one back.

// Host management modules.
const (
	HostModuleIdentity = "identity"
)

// HostModules lists the modules this version can manage.
var HostModules = []string{HostModuleIdentity}

// KindHostIdentity is the singleton host identity object.
const KindHostIdentity Kind = "HostIdentity"

// Limits of the identity module.
const (
	MaxHostnameLen     = 253
	MaxNameservers     = 3 // what the resolver reads (MAXNS)
	MaxSearchDomains   = 6 // MAXDNSRCH
	MaxSearchLen       = 256
	MaxNTPServers      = 4
	HostSnapshotLatest = "latest"
)

// HostIdentitySpec is what the identity module writes. A part left out (empty hostname, nil resolvers, nil ntp) is
// not changed; a part that is given replaces what the host has.
type HostIdentitySpec struct {
	// Hostname is one label or a fully qualified name (RFC 1123): set with hostname(1) and as hostname in
	// /etc/rc.conf.
	Hostname string `json:"hostname,omitempty" yaml:"hostname,omitempty"`
	// Resolvers is the content of /etc/resolv.conf.
	Resolvers *HostResolvers `json:"resolvers,omitempty" yaml:"resolvers,omitempty"`
	// NTP is the server list of the managed block of /etc/ntp.conf and whether ntpd runs.
	NTP *HostNTP `json:"ntp,omitempty" yaml:"ntp,omitempty"`
}

// HostResolvers is /etc/resolv.conf.
type HostResolvers struct {
	// Nameservers are IPv4 or IPv6 literals, 1 to 3.
	Nameservers []string `json:"nameservers" yaml:"nameservers"`
	// Search are domain names, at most 6 (256 characters together).
	Search []string `json:"search,omitempty" yaml:"search,omitempty"`
}

// HostNTP is the time service.
type HostNTP struct {
	// Servers are host names or IP literals, at most 4: the server lines between the jailops markers of
	// /etc/ntp.conf. Empty removes the block (the file's own pool and server lines stay).
	Servers []string `json:"servers" yaml:"servers"`
	// Enable sets ntpd_enable and ntpd_sync_on_start in /etc/rc.conf and starts or stops ntpd.
	Enable bool `json:"enable" yaml:"enable"`
}

// HostNTPSync is what ntpq reports, when it answers.
type HostNTPSync struct {
	// Synchronised is true when ntpd has selected a system peer.
	Synchronised bool `json:"synchronised"`
	// Peer is the selected server.
	Peer string `json:"peer,omitempty"`
	// Detail explains a state that is not synchronised.
	Detail string `json:"detail,omitempty"`
}

// HostIdentityStatus is what the host has and how it relates to JailOps.
type HostIdentityStatus struct {
	// Managed is true when both jailopsd.yaml and priv.conf enable the identity module.
	Managed bool `json:"managed"`
	// ConfigEnabled: host.manage.modules of jailopsd.yaml lists identity. HelperEnabled: host-manage of priv.conf does.
	ConfigEnabled bool `json:"config_enabled"`
	HelperEnabled bool `json:"helper_enabled"`
	// Drift names the parts ("hostname", "resolvers", "ntp") that differ from what JailOps last applied; empty
	// when nothing differs or nothing was applied.
	Drift []string `json:"drift,omitempty"`
	// Applied is what JailOps last applied (only the parts it was asked to change), and AppliedAt when.
	Applied   *HostIdentitySpec `json:"applied,omitempty"`
	AppliedAt *time.Time        `json:"applied_at,omitempty"`
	// RCHostname is the hostname /etc/rc.conf sets at boot.
	RCHostname string `json:"rc_hostname,omitempty"`
	// ResolvConfManaged: /etc/resolv.conf starts with the "# managed by jailops" header. NTPBlock: /etc/ntp.conf has
	// the jailops block.
	ResolvConfManaged bool `json:"resolv_conf_managed"`
	NTPBlock          bool `json:"ntp_block"`
	// NTPSync is omitted when ntpq could not be asked.
	NTPSync *HostNTPSync `json:"ntp_sync,omitempty"`
}

// HostIdentity is GET /api/v1/host/identity: the values read from the host (spec) and the management state.
type HostIdentity struct {
	Kind   Kind               `json:"kind"`
	Spec   HostIdentitySpec   `json:"spec"`
	Status HostIdentityStatus `json:"status"`
}

// HostIdentityApply is the body of PUT /api/v1/host/identity.
type HostIdentityApply struct {
	Spec HostIdentitySpec `json:"spec"`
	// DryRun returns the diff and the actions and changes nothing.
	DryRun bool `json:"dry_run,omitempty"`
}

// HostRestore is the body of POST /api/v1/host/snapshots/{name}/restore.
type HostRestore struct {
	DryRun bool `json:"dry_run,omitempty"`
}

// HostChange is the result of an apply or a restore.
type HostChange struct {
	DryRun bool `json:"dry_run,omitempty"`
	// Changed is false when the host already had what was asked for.
	Changed bool `json:"changed"`
	// Diff is a unified diff of the files JailOps renders (/etc/resolv.conf, /etc/ntp.conf); the /etc/rc.conf
	// variables set through sysrc appear as -/+ lines of those variables only.
	Diff string `json:"diff,omitempty"`
	// Actions lists what was done, or would be done, in order.
	Actions []string `json:"actions"`
	// Snapshot names the copy of the touched files taken before the change (restore it with
	// POST /api/v1/host/snapshots/{name}/restore).
	Snapshot string `json:"snapshot,omitempty"`
}

// HostSnapshot is a copy of the host files one module touched, taken before a change.
type HostSnapshot struct {
	// Name is the UTC time of the copy, 20261030T101500Z (a suffix -N keeps two in one second apart).
	Name   string    `json:"name"`
	Module string    `json:"module"`
	Time   time.Time `json:"time"`
	// Files are the host paths it holds.
	Files []string `json:"files"`
	// Reason is "apply" or "restore" (the state before a restore).
	Reason string `json:"reason,omitempty"`
}

var (
	reDNSLabel     = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)
	reHostSnapshot = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z(-[0-9]{1,3})?$`)
)

// ValidHostSnapshotName accepts a snapshot name, or HostSnapshotLatest.
func ValidHostSnapshotName(name string) error {
	if name == HostSnapshotLatest || reHostSnapshot.MatchString(name) {
		return nil
	}
	return Errorf(CodeInvalid, "invalid snapshot name %q", name).WithHint("use a name from GET /api/v1/host/snapshots, or latest")
}

// ValidDNSName accepts a host or domain name of RFC 1123 labels, at most 253 characters, that is not an IP address.
func ValidDNSName(name string) bool {
	if name == "" || len(name) > MaxHostnameLen {
		return false
	}
	if _, err := netip.ParseAddr(name); err == nil {
		return false
	}
	for _, l := range strings.Split(name, ".") {
		if !reDNSLabel.MatchString(l) {
			return false
		}
	}
	return true
}

// parseHostAddr accepts an IPv4 or IPv6 literal without a zone that names a host (not the unspecified address).
func parseHostAddr(s string) (netip.Addr, bool) {
	a, err := netip.ParseAddr(s)
	if err != nil || a.Zone() != "" || a.IsUnspecified() || a.Is4In6() {
		return netip.Addr{}, false
	}
	return a, true
}

// Validate checks the spec. It is run by the client, the daemon and again by the privileged helper.
func (s HostIdentitySpec) Validate() error {
	if s.Hostname == "" && s.Resolvers == nil && s.NTP == nil {
		return Errorf(CodeInvalid, "the identity names nothing to change").WithHint("give a hostname, resolvers or ntp")
	}
	if s.Hostname != "" && !ValidDNSName(s.Hostname) {
		return Errorf(CodeInvalid, "hostname %q is not a valid host name", s.Hostname).
			WithHint("letters, digits and hyphens in labels of at most 63 characters, separated by dots, at most 253 in all; not an IP address")
	}
	if r := s.Resolvers; r != nil {
		if len(r.Nameservers) == 0 || len(r.Nameservers) > MaxNameservers {
			return Errorf(CodeInvalid, "resolvers need 1 to %d nameservers, not %d", MaxNameservers, len(r.Nameservers))
		}
		var seen []netip.Addr
		for _, n := range r.Nameservers {
			a, ok := parseHostAddr(n)
			if !ok {
				return Errorf(CodeInvalid, "nameserver %q is not an IPv4 or IPv6 address", n).WithHint("a literal such as 192.0.2.53 or 2001:db8::53, without a zone")
			}
			if slices.Contains(seen, a) {
				return Errorf(CodeInvalid, "nameserver %s is listed twice", n)
			}
			seen = append(seen, a)
		}
		if len(r.Search) > MaxSearchDomains {
			return Errorf(CodeInvalid, "at most %d search domains, not %d", MaxSearchDomains, len(r.Search))
		}
		total := 0
		var seenD []string
		for _, d := range r.Search {
			if !ValidDNSName(d) {
				return Errorf(CodeInvalid, "search domain %q is not a valid domain name", d)
			}
			if slices.Contains(seenD, strings.ToLower(d)) {
				return Errorf(CodeInvalid, "search domain %s is listed twice", d)
			}
			seenD = append(seenD, strings.ToLower(d))
			total += len(d) + 1
		}
		if total > MaxSearchLen {
			return Errorf(CodeInvalid, "the search domains are longer than %d characters together", MaxSearchLen)
		}
	}
	if n := s.NTP; n != nil {
		if len(n.Servers) > MaxNTPServers {
			return Errorf(CodeInvalid, "at most %d NTP servers, not %d", MaxNTPServers, len(n.Servers))
		}
		var seen []string
		for _, v := range n.Servers {
			key := strings.ToLower(v)
			if a, ok := parseHostAddr(v); ok {
				key = a.String()
			} else if !ValidDNSName(v) {
				return Errorf(CodeInvalid, "NTP server %q is neither a host name nor an IP address", v)
			}
			if slices.Contains(seen, key) {
				return Errorf(CodeInvalid, "NTP server %s is listed twice", v)
			}
			seen = append(seen, key)
		}
	}
	return nil
}

// Canonical returns the spec with addresses in their canonical form (2001:db8::53, not 2001:0db8::0053). Call it
// after Validate.
func (s HostIdentitySpec) Canonical() HostIdentitySpec {
	out := HostIdentitySpec{Hostname: s.Hostname}
	if r := s.Resolvers; r != nil {
		c := HostResolvers{Nameservers: make([]string, 0, len(r.Nameservers)), Search: append([]string(nil), r.Search...)}
		for _, n := range r.Nameservers {
			if a, ok := parseHostAddr(n); ok {
				n = a.String()
			}
			c.Nameservers = append(c.Nameservers, n)
		}
		out.Resolvers = &c
	}
	if n := s.NTP; n != nil {
		c := HostNTP{Enable: n.Enable, Servers: make([]string, 0, len(n.Servers))}
		for _, v := range n.Servers {
			if a, ok := parseHostAddr(v); ok {
				v = a.String()
			}
			c.Servers = append(c.Servers, v)
		}
		out.NTP = &c
	}
	return out
}
