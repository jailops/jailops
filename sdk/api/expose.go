package api

// ExposeRequest is the body of POST /api/v1/jails/{name}/expose (extension jail_expose): the ports of the jail that the
// Internet reaches. An entry for the same protocol and ports is replaced.
type ExposeRequest struct {
	// Proto is tcp (the default), udp, or tcp,udp.
	Proto string `json:"proto,omitempty"`
	// Ports are "80", "80,443" as a list, or a range "8000-8100" (a range is not forwarded).
	Ports []string `json:"ports"`
	// From restricts who may connect: addresses, prefixes and address sets (set:NAME); empty is anyone.
	From []string `json:"from,omitempty"`
	// Families are "4" and "6"; empty is every family the jail has.
	Families []string `json:"families,omitempty"`
	// HostPort is the host port of the IPv4 forward when it differs from the jail's port (one port only).
	HostPort int `json:"host_port,omitempty"`
	// Listen is the host address of the IPv4 forward; empty is the uplink's address.
	Listen  string `json:"listen,omitempty"`
	Comment string `json:"comment,omitempty"`
	// DryRun says what would happen and stores nothing.
	DryRun bool `json:"dry_run,omitempty"`
}

// UnexposeRequest is the body of POST /api/v1/jails/{name}/unexpose. Without ports it removes the whole exposure of the
// protocol (or of every protocol); with ports it takes those ports out; with families it takes those families out.
type UnexposeRequest struct {
	Proto    string   `json:"proto,omitempty"`
	Ports    []string `json:"ports,omitempty"`
	Families []string `json:"families,omitempty"`
	DryRun   bool     `json:"dry_run,omitempty"`
}

// ExposeResult is what expose and unexpose did to a jail.
type ExposeResult struct {
	Jail string `json:"jail"`
	// Changed is false when the exposure was already what the request asks for.
	Changed bool `json:"changed"`
	// Entries is the jail's exposure afterwards.
	Entries []ExposeEntry `json:"entries"`
	// Families says, for every address of the jail, how the Internet reaches the ports of the request.
	Families []ExposeFamily `json:"families"`
	Warnings []string       `json:"warnings,omitempty"`
}

// ExposeFamily is how the Internet reaches one address of a jail.
type ExposeFamily struct {
	// Family is "4" or "6".
	Family  string `json:"family"`
	Address string `json:"address"`
	Network string `json:"network"`
	// Interface is the uplink the traffic arrives on.
	Interface string `json:"interface,omitempty"`
	// Via is "uplink" (the address is reached directly), "forward" (an IPv4 address behind NAT: host ports are
	// forwarded to it) or "none" (the network has no uplink: nothing arrives from outside).
	Via string `json:"via"`
	// Ports are the jail's ports of the request, as text ("80, 443").
	Ports string `json:"ports"`
	// Forwards are the host ports of a forward as "HOST->JAIL" pairs.
	Forwards []string `json:"forwards,omitempty"`
	// Listen is the host address of a forward; empty is the uplink's address.
	Listen string `json:"listen,omitempty"`
	// Note says why a family is not reached ("the jail has no IPv6 address").
	Note string `json:"note,omitempty"`
}
