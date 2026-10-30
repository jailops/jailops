package wireguard

import (
	"net/netip"

	"jailops.net/jailops/sdk/api"
)

// NextAddress returns the lowest address of pool that is not in used. The first address of the pool (the IPv4
// network address, the IPv6 subnet-router anycast address) and the last address of an IPv4 pool shorter than /31
// (broadcast) are never handed out. ok is false when the pool is full.
func NextAddress(pool netip.Prefix, used []netip.Addr) (netip.Addr, bool) {
	pool = pool.Masked()
	taken := make(map[netip.Addr]bool, len(used))
	for _, a := range used {
		taken[a.Unmap()] = true
	}
	a := pool.Addr()
	// Only len(used)+1 candidates can be taken before a free one turns up; bound the walk by that.
	for range len(used) + 2 {
		a = a.Next()
		if !a.IsValid() || !pool.Contains(a) {
			return netip.Addr{}, false
		}
		if a.Is4() && pool.Bits() < 31 && !pool.Contains(a.Next()) {
			return netip.Addr{}, false // the broadcast address
		}
		if !taken[a] {
			return a, true
		}
	}
	return netip.Addr{}, false
}

// PeerAddresses assigns a new peer one address of each family of the pool (as /32 and /128), skipping the
// server's own addresses and those of the other peers.
func PeerAddresses(spec api.WireGuardSpec) ([]netip.Prefix, error) {
	pool, err := ParsePrefixes("allowed_ips", spec.AllowedIPs, false)
	if err != nil {
		return nil, err
	}
	used, err := UsedAddresses(spec)
	if err != nil {
		return nil, err
	}
	var out []netip.Prefix
	have4, have6 := false, false
	for _, p := range pool {
		if p.Addr().Is4() && have4 || p.Addr().Is6() && have6 {
			continue
		}
		a, ok := NextAddress(p, used)
		if !ok {
			continue
		}
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
		if a.Is4() {
			have4 = true
		} else {
			have6 = true
		}
	}
	if len(out) == 0 {
		return nil, api.Errorf(api.CodePoolExhausted, "the address pool of the tunnel is full").WithHint("widen allowed_ips (the pool) or remove a peer")
	}
	return out, nil
}

// UsedAddresses lists the addresses of the server and of its peers.
func UsedAddresses(spec api.WireGuardSpec) ([]netip.Addr, error) {
	var used []netip.Addr
	addrs, err := ParsePrefixes("address", spec.Address, false)
	if err != nil {
		return nil, err
	}
	for _, a := range addrs {
		used = append(used, a.Addr())
	}
	for _, p := range spec.Peers {
		pa, err := ParsePrefixes("allowed_ips", p.AllowedIPs, true)
		if err != nil {
			return nil, err
		}
		for _, x := range pa {
			used = append(used, x.Addr())
		}
	}
	return used, nil
}
