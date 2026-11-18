package ipam

import (
	"net/netip"
	"slices"

	"jailops.net/jailops/sdk/api"
)

// AnswerLimit6 is how many addresses of an IPv6 allocation range the proxy of a network answers for.
const AnswerLimit6 = api.AnswerLimit6

// ProxyAddrs6 lists the addresses the neighbor discovery proxy answers for on a network with the NDP proxy: the first
// AnswerLimit6 addresses of the allocation range, minus the excluded spans, the resolver address of the network and the
// base address of the prefix (the subnet-router anycast address, which a router uses). More reports whether the range
// has addresses beyond the limit. The addresses of the host itself are not known here; the proxy and the helper leave
// them out. An excluded span is jumped over, so the work is bounded by the limit and the number of spans, not by the
// size of the range.
func ProxyAddrs6(v6 *api.IPv6Net) (addrs []netip.Addr, more bool, err error) {
	if v6 == nil || v6.Range == "" {
		return nil, false, nil
	}
	rng, err := ParseRange(v6.Range)
	if err != nil {
		return nil, false, err
	}
	excl, err := ParseExclusions(v6.Exclude)
	if err != nil {
		return nil, false, err
	}
	var resolver, base netip.Addr
	if p, perr := netip.ParsePrefix(v6.Subnet); perr == nil {
		resolver, base = Resolver6(p), p.Masked().Addr()
	}
	for a := rng.First; a.IsValid() && !rng.Last.Less(a); {
		if len(addrs) >= AnswerLimit6 {
			return addrs, true, nil
		}
		if a == resolver || a == base {
			a = a.Next()
			continue
		}
		if e, ok := excludedSpan(excl, a); ok {
			a = e.Last.Next()
			continue
		}
		addrs = append(addrs, a)
		a = a.Next()
	}
	return addrs, false, nil
}

// excludedSpan returns the excluded span that holds a.
func excludedSpan(excl []Range, a netip.Addr) (Range, bool) {
	for _, e := range excl {
		if e.Contains(a) {
			return e, true
		}
	}
	return Range{}, false
}

// InProxyRange6 reports whether a is one of the addresses ProxyAddrs6 lists: inside the allocation range of v6, not
// excluded, not the resolver or the base address, and among the first AnswerLimit6. What lies beyond the limit is not
// answered for by the range; the address of a jail that runs is (the helper's route check).
func InProxyRange6(v6 *api.IPv6Net, a netip.Addr) bool {
	addrs, _, err := ProxyAddrs6(v6)
	return err == nil && slices.Contains(addrs, a)
}
