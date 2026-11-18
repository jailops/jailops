package service

import (
	"slices"
	"testing"

	"jailops.net/jailops/sdk/api"
)

// While the names of the jails are on, a jail asks the host in the address families of its network: the IPv4 gateway,
// the resolver address of IPv6 (the second address of the subnet), or both, IPv4 first.
func TestResolversFollowTheAddressFamiliesOfTheNetwork(t *testing.T) {
	s := &Service{}
	s.unbound.known, s.unbound.enabled = true, true
	v4 := &api.IPv4Net{Subnet: "10.20.0.0/24", Gateway: "10.20.0.1"}
	v6 := &api.IPv6Net{Subnet: "2001:db8:1:2:100::/80"}
	for name, c := range map[string]struct {
		spec api.NetworkSpec
		want []string
	}{
		"IPv4 only":  {api.NetworkSpec{IPv4: v4}, []string{"10.20.0.1"}},
		"dual stack": {api.NetworkSpec{IPv4: v4, IPv6: v6}, []string{"10.20.0.1", "2001:db8:1:2:100::1"}},
		"IPv6 only":  {api.NetworkSpec{IPv6: v6}, []string{"2001:db8:1:2:100::1"}},
	} {
		src, got := s.resolvers([]api.NetworkSpec{c.spec})
		var have []string
		for _, a := range got {
			have = append(have, a.String())
		}
		if src != api.DNSFromGateway || !slices.Equal(have, c.want) {
			t.Errorf("%s: %s %v, want %v", name, src, have, c.want)
		}
	}
}
