package api

// Network is a managed VNET bridge with deterministic address allocation.
// jailops creates the bridge and the epair interfaces, configures addresses
// and routes inside the jail, and installs the host-side addresses. There is
// no DHCP, no router advertisement and no SLAAC.
type Network struct {
	Metadata ObjectMeta    `json:"metadata"`
	Spec     NetworkSpec   `json:"spec"`
	Status   NetworkStatus `json:"status"`
}

// NetworkSpec is the desired state of a network.
type NetworkSpec struct {
	// Bridge is the if_bridge name. Defaults to a generated "jo<n>" name.
	// Bridges not created by jailops are never modified.
	Bridge string `json:"bridge,omitempty"`
	// Uplink is the physical interface used for NAT or routing. In IPv6
	// proxy mode it must never be a member of the bridge.
	Uplink string   `json:"uplink,omitempty"`
	IPv4   *IPv4Net `json:"ipv4,omitempty"`
	IPv6   *IPv6Net `json:"ipv6,omitempty"`
	DNS    []string `json:"dns,omitempty"`
	// MTU of the bridge and its members; zero keeps the default.
	MTU int `json:"mtu,omitempty"`
	// Private makes every jail interface a private member of the bridge
	// (ifconfig <bridge> private <epair>). A private member cannot talk to any
	// other member, not even a jail of the same project on the same subnet:
	// all traffic goes through the host. Use it only for networks routed
	// through the host (uplink set); jails on one subnet cannot reach each
	// other. A network belongs to one project, so there is no addressing to
	// share. Default false.
	Private bool `json:"private,omitempty"`
}

// IPv4Net configures IPv4 on a network.
type IPv4Net struct {
	Subnet string `json:"subnet"` // 10.20.0.0/24
	// Gateway is a real address owned by the host, configured on the host end (<bridge>gb) of a gateway epair whose
	// other end is a port of the bridge; the bridge itself carries no address.
	Gateway string `json:"gateway"`
	// Range is the inclusive span jailops may allocate from, "a-b".
	Range string `json:"range"`
	// Exclude removes spans or single addresses from the range.
	Exclude []string `json:"exclude,omitempty"`
	// NAT masquerades traffic leaving through the uplink.
	NAT bool `json:"nat,omitempty"`
}

// IPv6Mode selects how jail addresses reach the upstream network.
type IPv6Mode string

const (
	// IPv6Routed: the upstream routes a prefix to the host. The bridge owns
	// the prefix and no NDP proxy is involved.
	IPv6Routed IPv6Mode = "routed"
	// IPv6Proxy: the upstream treats the jail addresses as on-link. jailops
	// answers neighbor solicitations for allocated addresses on the uplink
	// and routes /128 host routes through the bridge.
	IPv6Proxy IPv6Mode = "proxy"
	IPv6None  IPv6Mode = "none"
)

// IPv6Net configures IPv6 on a network.
type IPv6Net struct {
	Mode    IPv6Mode `json:"mode"`
	Subnet  string   `json:"subnet"`
	Gateway string   `json:"gateway"` // link-local is allowed
	Range   string   `json:"range"`
	Exclude []string `json:"exclude,omitempty"`
}

// NetworkStatus is the observed state of a network on one host.
type NetworkStatus struct {
	BridgeUp  bool     `json:"bridge_up,omitempty"`
	Members   []string `json:"members,omitempty"`
	Allocated int      `json:"allocated,omitempty"`
	Free      int      `json:"free,omitempty"`
	// Orphans are epair interfaces without a jail.
	Orphans    []string    `json:"orphans,omitempty"`
	Conditions []Condition `json:"conditions,omitempty"`
	// Warnings are set in the answer of a create only: what the host lacks for
	// this network (a sysctl the administrator has to set). They are never
	// stored; jailops doctor reports the same things.
	Warnings []string `json:"warnings,omitempty"`
}

// Lease is one allocated address. Leases are derived from jail specs and are
// reported for inspection; the jail spec is the source of truth.
type Lease struct {
	Address string `json:"address"`
	Jail    string `json:"jail"`
	Network string `json:"network"`
}
