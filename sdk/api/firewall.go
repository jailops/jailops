package api

// FirewallPolicy is a declarative set of rules rendered into one PF anchor
// per jail ("jailops/<jail>"). jailops never owns the host's pf.conf; the
// administrator adds the anchor lines once.
type FirewallPolicy struct {
	Metadata ObjectMeta         `json:"metadata"`
	Spec     FirewallPolicySpec `json:"spec"`
}

// FirewallPolicySpec holds the rules of a policy.
type FirewallPolicySpec struct {
	// Default applies when no rule matches: "allow" or "deny".
	DefaultIn  string         `json:"default_in,omitempty"`
	DefaultOut string         `json:"default_out,omitempty"`
	Rules      []FirewallRule `json:"rules,omitempty"`
	// Raw is a PF fragment. It requires the admin role and is validated with
	// pfctl -n before it is loaded.
	Raw string `json:"raw,omitempty"`
}

// FirewallRule is one typed rule.
type FirewallRule struct {
	Name      string   `json:"name,omitempty"`
	Action    string   `json:"action"`          // allow, deny
	Direction string   `json:"direction"`       // in, out
	Proto     string   `json:"proto,omitempty"` // tcp, udp, icmp, icmp6
	From      []string `json:"from,omitempty"`  // addresses, @address-set, or tag:<tag>
	To        []string `json:"to,omitempty"`
	Ports     []string `json:"ports,omitempty"` // "80", "8000-8100"
	Log       bool     `json:"log,omitempty"`
	// Redirect rewrites the destination (rdr). Allowed on inbound rules only.
	Redirect *Redirect `json:"redirect,omitempty"`
}

// Redirect is a port forward to the jail.
type Redirect struct {
	// Listen is the host address the port is forwarded on; empty means the
	// uplink address.
	Listen   string `json:"listen,omitempty"`
	HostPort int    `json:"host_port"`
	JailPort int    `json:"jail_port"`
}

// AddressSet is a named list of addresses referenced as @name in rules.
type AddressSet struct {
	Metadata ObjectMeta     `json:"metadata"`
	Spec     AddressSetSpec `json:"spec"`
}

// AddressSetSpec lists the members of an address set.
type AddressSetSpec struct {
	Addresses []string `json:"addresses"`
	// Includes names other address sets; cycles are rejected.
	Includes []string `json:"includes,omitempty"`
}
