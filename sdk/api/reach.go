package api

// ExposedEntry is one way the Internet reaches a jail: a pass rule on an uplink (an allow in rule with an external
// source, on an address the upstream routes to the host) or a port forward of the host. Reach from a WireGuard tunnel is
// not listed here (see ReachRow).
type ExposedEntry struct {
	// Proto is "tcp", "udp", "icmp" or "icmp6".
	Proto string `json:"proto"`
	// Ports is the destination port list on the jail ("80,443", "8000-8100"); empty for every port, and for icmp.
	Ports string `json:"ports,omitempty"`
	// Family is "4" or "6".
	Family string `json:"family"`
	// Address is the jail's address the traffic is for.
	Address string `json:"address"`
	// HostAddress and HostPort are the host's address and port of a forward; empty and zero for a pass rule.
	HostAddress string `json:"host_address,omitempty"`
	HostPort    int    `json:"host_port,omitempty"`
	// From is what the rule names as the source: "any", an address, a prefix or set:NAME.
	From []string `json:"from"`
	// Restricted is true when the source is not "any".
	Restricted bool `json:"restricted"`
	// Via is "uplink" for a pass rule and "forward" for a port forward.
	Via       string `json:"via"`
	Interface string `json:"interface,omitempty"`
	Network   string `json:"network,omitempty"`
	// By says where it comes from: "policy NAME rule N" (with the rule's name when it has one).
	By string `json:"by"`
}

// ExposedJail is an exposed entry with its jail, for the host-wide view.
type ExposedJail struct {
	Project string `json:"project"`
	Jail    string `json:"jail"`
	ExposedEntry
}

// Exposed is the reply of GET /api/v1/exposed: everything the Internet reaches, across the jails the caller can see.
type Exposed struct {
	Entries []ExposedJail `json:"entries"`
	// Tunnels are the rows reachable from WireGuard peers; only filled when asked for (?tunnels=true).
	Tunnels []ExposedJail `json:"tunnels,omitempty"`
	// Errors are the jails whose anchor could not be built, with the reason.
	Errors []string `json:"errors,omitempty"`
}

// Sources of a reach row.
const (
	ReachInternet = "internet"
	ReachTunnel   = "tunnel"
	ReachJails    = "jails"
	ReachHost     = "host"
	ReachOther    = "other-projects"
)

// ReachRow says what one kind of source reaches on a jail.
type ReachRow struct {
	// Source is one of the Reach constants; SourceName names the tunnel interface of a tunnel row.
	Source     string `json:"source"`
	SourceName string `json:"source_name,omitempty"`
	// Family is "4", "6" or "4,6".
	Family string `json:"family"`
	Proto  string `json:"proto"`
	// Ports is the destination port list; "all" for every port; empty when nothing is reachable.
	Ports string `json:"ports,omitempty"`
	// From is the source restriction of the rule (empty: any source of the kind).
	From       []string `json:"from,omitempty"`
	Restricted bool     `json:"restricted,omitempty"`
	// Decision is "pass", "forward" or "none" (nothing of this kind reaches the jail; By says what stops it).
	Decision string `json:"decision"`
	// By says what gives the reach, or what stops it.
	By string `json:"by"`
	// Policy, Rule and RuleName identify the rule of a policy; Stage is the stage of the anchor.
	Policy   string `json:"policy,omitempty"`
	Rule     int    `json:"rule,omitempty"`
	RuleName string `json:"rule_name,omitempty"`
	Stage    string `json:"stage,omitempty"`
	// HostAddress and HostPort belong to a forward.
	HostAddress string `json:"host_address,omitempty"`
	HostPort    int    `json:"host_port,omitempty"`
	Interface   string `json:"interface,omitempty"`
}

// ReachRule is one rule of the anchor, in the order pf decides, for `firewall show`.
type ReachRule struct {
	// Where is "uplink NAME", "tunnel NAME" or "link".
	Where string `json:"where"`
	// Direction is "to" (what arrives at the jail) or "from" (what the jail sends).
	Direction string `json:"direction"`
	Family    string `json:"family"`
	From      string `json:"from"`
	To        string `json:"to,omitempty"`
	Proto     string `json:"proto,omitempty"`
	Ports     string `json:"ports,omitempty"`
	// Decision is "pass", "block", "forward", "disabled" or "raw".
	Decision string `json:"decision"`
	Stage    string `json:"stage"`
	By       string `json:"by"`
	// PF is the rendered line of the anchor.
	PF string `json:"pf,omitempty"`
}

// ReachOut says what a jail can reach.
type ReachOut struct {
	Internet      string `json:"internet"`
	Host          string `json:"host"`
	Jails         string `json:"jails"`
	OtherProjects string `json:"other_projects"`
}

// ReachPolicy is one policy of a jail's firewall: where it comes from, how many rules it has and the defaults it states
// (empty: it states none).
type ReachPolicy struct {
	Name string `json:"name"`
	// Source is "project" (a mandatory policy of the project), "managed" (kept by app access) or "jail" (named in the
	// jail's firewall.policies).
	Source     string `json:"source"`
	Rules      int    `json:"rules"`
	DefaultIn  string `json:"default_in,omitempty"`
	DefaultOut string `json:"default_out,omitempty"`
}

// JailReach is the reply of GET /api/v1/jails/{name}/reach: what reaches a jail, what the jail reaches, and the rules in
// the order pf decides. It is computed from the anchor the renderer builds for the jail.
type JailReach struct {
	Project string `json:"project"`
	Jail    string `json:"jail"`
	// Policies are the policies of the jail in the order they are rendered: the project's, the managed one of app
	// access, then the jail's own.
	Policies []ReachPolicy `json:"policies,omitempty"`
	// Rows are what each kind of source reaches (inbound).
	Rows []ReachRow `json:"rows"`
	// Exposed is what the Internet reaches (the rows of the sources "internet", as the entries of the jail list).
	Exposed []ExposedEntry `json:"exposed,omitempty"`
	// Out is what the jail reaches.
	Out ReachOut `json:"out"`
	// DefaultIn and DefaultOut are "allow" or "deny": what the anchor does with traffic no rule names. A default is deny when
	// any policy of the jail states deny, else allow.
	DefaultIn  string `json:"default_in"`
	DefaultOut string `json:"default_out"`
	// DefaultInBy and DefaultOutBy are the policies that state deny for the direction ("name", with " [project]" or
	// " [managed]" after the project's and the app's); empty when none does and the default is allow.
	DefaultInBy  []string `json:"default_in_by,omitempty"`
	DefaultOutBy []string `json:"default_out_by,omitempty"`
	// Rules are the rules of the anchor in evaluation order.
	Rules []ReachRule `json:"rules"`
	// RulesID names the rules the policies render now (twelve hex digits). The FirewallApplied condition of the jail
	// carries the id of the rules the kernel was last confirmed to have: the two are equal when the kernel has what the
	// policies say.
	RulesID string `json:"rules_id,omitempty"`
	// Error says why the anchor could not be built; the other fields are then empty.
	Error string `json:"error,omitempty"`
	// Notes say what the computation cannot know (a raw PF fragment, the host's own rules).
	Notes []string `json:"notes,omitempty"`
}
