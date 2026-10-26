package ipam

import (
	"math/big"
	"net/netip"
	"sort"
	"strings"

	"jailops.net/jailops/sdk/api"
)

// Allocator hands out addresses from one range of one subnet. It never hands
// out the network address, the IPv4 broadcast address or the gateway, and it
// skips exclusions. It is not safe for concurrent use; callers hold the
// network lock.
type Allocator struct {
	prefix   netip.Prefix
	gateway  netip.Addr
	rng      Range
	excl     []Range // merged
	reserved map[netip.Addr]bool
	used     map[netip.Addr]string
	// sub is the project's sub-range (valid when hasSub); fixed addresses
	// must lie in it. scopeEx are other projects' ranges, which a project
	// without a sub-range must not take fixed addresses from.
	sub     Range
	hasSub  bool
	scopeEx []Range
}

// New builds an Allocator. gateway may be invalid (none) or outside the
// subnet (a link-local IPv6 gateway); inUse lists addresses already taken.
func New(prefix netip.Prefix, gateway netip.Addr, rng Range, exclude []Range, inUse []netip.Addr) (*Allocator, error) {
	prefix = prefix.Masked()
	if !prefix.IsValid() {
		return nil, invalid("subnet", "invalid subnet")
	}
	if rng.First.BitLen() != prefix.Addr().BitLen() || !prefix.Contains(rng.First) || !prefix.Contains(rng.Last) {
		return nil, invalid("range", "range %s-%s is outside subnet %s", rng.First, rng.Last, prefix)
	}
	a := &Allocator{
		prefix:   prefix,
		rng:      rng,
		excl:     merge(exclude),
		used:     map[netip.Addr]string{},
		reserved: map[netip.Addr]bool{},
	}
	a.reserved[prefix.Addr()] = true // network address (v6: subnet-router anycast)
	if prefix.Addr().Is4() && prefix.Bits() <= 30 {
		a.reserved[lastOf(prefix)] = true // broadcast
	}
	if prefix.Addr().Is6() {
		// The all-ones end of an IPv6 subnet is kept back as broadcast-like (nothing uses it, and some stacks treat it
		// as special). In a /64 the reserved subnet anycast identifiers of RFC 2526 are reserved too: the highest 128
		// interface identifiers, and the same 128 with the universal/local bit cleared (fdff:ffff:ffff:ff80 to
		// fdff:ffff:ffff:ffff, the form the RFC writes for EUI-64 interfaces). Other prefix lengths have no such rule.
		last := lastOf(prefix)
		a.reserved[last] = true
		if prefix.Bits() == 64 {
			for i := 0; i < 128; i++ {
				c := last.As16()
				c[15] = byte(0x80 + i)
				a.reserved[netip.AddrFrom16(c)] = true
				c[8] &^= 0x02
				a.reserved[netip.AddrFrom16(c)] = true
			}
		}
	}
	if gateway.IsValid() {
		a.gateway = norm(gateway)
		a.reserved[a.gateway] = true
	}
	for _, u := range inUse {
		a.used[norm(u)] = ""
	}
	return a, nil
}

// NewV4 builds an Allocator from the network's IPv4 configuration.
func NewV4(n *api.IPv4Net, inUse []netip.Addr) (*Allocator, error) {
	return NewV4In(n, Scope{}, inUse)
}

// Scope narrows an allocator to one project. Sub is the project's sub-range
// ("a-b" or "a"; empty means the whole network range). Exclude lists the
// sub-ranges of other projects; it applies only when Sub is empty.
type Scope struct {
	Sub     string
	Exclude []string
}

// NewV4In is NewV4 restricted to a project's scope.
func NewV4In(n *api.IPv4Net, sc Scope, inUse []netip.Addr) (*Allocator, error) {
	if n == nil {
		return nil, invalid("ipv4", "network has no IPv4 configuration")
	}
	return fromStrings("ipv4", n.Subnet, n.Gateway, n.Range, n.Exclude, sc, inUse)
}

// NewV6 builds an Allocator from the network's IPv6 configuration.
func NewV6(n *api.IPv6Net, inUse []netip.Addr) (*Allocator, error) {
	return NewV6In(n, Scope{}, inUse)
}

// NewV6In is NewV6 restricted to a project's scope.
func NewV6In(n *api.IPv6Net, sc Scope, inUse []netip.Addr) (*Allocator, error) {
	if n == nil || n.Mode == api.IPv6None {
		return nil, invalid("ipv6", "network has no IPv6 configuration")
	}
	return fromStrings("ipv6", n.Subnet, n.Gateway, n.Range, n.Exclude, sc, inUse)
}

func fromStrings(field, subnet, gw, rng string, excl []string, sc Scope, inUse []netip.Addr) (*Allocator, error) {
	p, err := netip.ParsePrefix(subnet)
	if err != nil {
		return nil, invalid(field+".subnet", "bad subnet %q", subnet)
	}
	var g netip.Addr
	if gw != "" {
		if g, err = parseAddr(gw); err != nil {
			return nil, invalid(field+".gateway", "bad gateway %q", gw)
		}
	}
	r, err := ParseRange(rng)
	if err != nil {
		return nil, err
	}
	ex, err := ParseExclusions(excl)
	if err != nil {
		return nil, err
	}
	var sub Range
	hasSub := strings.TrimSpace(sc.Sub) != ""
	var scopeEx []Range
	if hasSub {
		if sub, err = ParseRange(sc.Sub); err != nil {
			return nil, err
		}
		if sub.First.BitLen() != r.First.BitLen() {
			return nil, invalid("range", "the project's range %q has the wrong address family", sc.Sub)
		}
		lo, hi := r.First, r.Last
		if lo.Less(sub.First) {
			lo = sub.First
		}
		if sub.Last.Less(hi) {
			hi = sub.Last
		}
		if hi.Less(lo) {
			return nil, invalid("range", "the project's range is outside the network's allocation range")
		}
		r = Range{lo, hi}
	} else if len(sc.Exclude) > 0 {
		if scopeEx, err = ParseExclusions(sc.Exclude); err != nil {
			return nil, err
		}
		ex = append(ex, scopeEx...)
	}
	a, err := New(p, g, r, ex, inUse)
	if err != nil {
		return nil, err
	}
	a.sub, a.hasSub, a.scopeEx = sub, hasSub, merge(scopeEx)
	return a, nil
}

// ParseSub parses a project's sub-range and checks that it lies inside the
// network's allocation range netRange.
func ParseSub(sub, netRange string) (Range, error) {
	s, err := ParseRange(sub)
	if err != nil {
		return Range{}, err
	}
	n, err := ParseRange(netRange)
	if err != nil {
		return Range{}, err
	}
	if s.First.BitLen() != n.First.BitLen() || s.First.Less(n.First) || n.Last.Less(s.Last) {
		return Range{}, invalid("range", "range %s-%s is outside the network's range %s-%s", s.First, s.Last, n.First, n.Last)
	}
	return s, nil
}

// Overlap reports whether two ranges of one family share an address.
func Overlap(a, b Range) bool {
	if a.First.BitLen() != b.First.BitLen() {
		return false
	}
	return !a.Last.Less(b.First) && !b.Last.Less(a.First)
}

// scopeCheck refuses a fixed address outside the project's scope.
func (a *Allocator) scopeCheck(x netip.Addr) error {
	forbid := func(why string) error {
		return api.Errorf(api.CodeForbidden, "address %s is %s", x, why).
			WithDetail("address", x.String()).WithDetail("range", why)
	}
	if a.hasSub {
		if !a.sub.Contains(x) {
			return forbid("outside the project's range " + a.sub.First.String() + "-" + a.sub.Last.String())
		}
		return nil
	}
	for _, r := range a.scopeEx {
		if r.Contains(x) {
			return forbid("in a range reserved for another project")
		}
	}
	return nil
}

// Prefix returns the subnet.
func (a *Allocator) Prefix() netip.Prefix { return a.prefix }

// Reserve marks addr as used by owner. A second reservation of the same
// address returns CodeIPConflict. owner may be empty.
func (a *Allocator) Reserve(addr netip.Addr, owner string) error {
	_, err := a.take(norm(addr), owner)
	return err
}

// Release frees an address.
func (a *Allocator) Release(addr netip.Addr) { delete(a.used, norm(addr)) }

// Allocate returns preferred if it is valid and free, or the lowest free
// address of the range when preferred is the zero Addr. The address is marked
// used under owner.
func (a *Allocator) Allocate(preferred netip.Addr, owner string) (netip.Addr, error) {
	if preferred.IsValid() {
		if err := a.scopeCheck(norm(preferred)); err != nil {
			return netip.Addr{}, err
		}
		return a.take(norm(preferred), owner)
	}
	cand := a.rng.First
	for {
		if !cand.IsValid() || a.rng.Last.Less(cand) {
			return netip.Addr{}, api.Errorf(api.CodePoolExhausted, "no free address in %s-%s", a.rng.First, a.rng.Last).
				WithDetail("subnet", a.prefix.String()).
				WithHint("widen the range or free an address")
		}
		if e, ok := a.excluded(cand); ok {
			cand = e.Last.Next()
			continue
		}
		if a.reserved[cand] {
			cand = cand.Next()
			continue
		}
		if _, taken := a.used[cand]; taken {
			cand = cand.Next()
			continue
		}
		a.used[cand] = owner
		return cand, nil
	}
}

func (a *Allocator) excluded(x netip.Addr) (Range, bool) {
	i := sort.Search(len(a.excl), func(i int) bool { return !a.excl[i].Last.Less(x) })
	if i < len(a.excl) && !x.Less(a.excl[i].First) {
		return a.excl[i], true
	}
	return Range{}, false
}

func (a *Allocator) take(x netip.Addr, owner string) (netip.Addr, error) {
	if !x.IsValid() || !a.prefix.Contains(x) {
		return netip.Addr{}, invalid("address", "address %s is outside subnet %s", x, a.prefix).WithDetail("address", x.String())
	}
	conflict := func(why string) error {
		return api.Errorf(api.CodeIPConflict, "address %s is not available: %s", x, why).
			WithDetail("address", x.String()).WithDetail("reason", why)
	}
	if a.reserved[x] {
		switch {
		case x == a.gateway:
			return netip.Addr{}, conflict("gateway")
		case x == a.prefix.Addr():
			return netip.Addr{}, conflict("network address")
		case x.Is6() && a.prefix.Bits() == 64 && x != lastOf(a.prefix):
			return netip.Addr{}, conflict("reserved subnet anycast address (RFC 2526)")
		default:
			return netip.Addr{}, conflict("broadcast address")
		}
	}
	if _, ok := a.excluded(x); ok {
		return netip.Addr{}, conflict("excluded")
	}
	if o, ok := a.used[x]; ok {
		e := conflict("in use").(*api.Error)
		if o != "" {
			e = e.WithDetail("owner", o)
		}
		return netip.Addr{}, e
	}
	a.used[x] = owner
	return x, nil
}

// Counts reports how many allocatable addresses of the range are used and
// free. Addresses outside the range (fixed addresses) are not counted.
func (a *Allocator) Counts() (allocated, free *big.Int) {
	total := a.rng.Size()
	// Remove exclusions and reserved addresses clipped to the range.
	blocked := append([]Range(nil), a.excl...)
	for r := range a.reserved {
		blocked = append(blocked, Range{r, r})
	}
	for _, b := range merge(blocked) {
		lo, hi := b.First, b.Last
		if lo.Less(a.rng.First) {
			lo = a.rng.First
		}
		if a.rng.Last.Less(hi) {
			hi = a.rng.Last
		}
		if hi.Less(lo) {
			continue
		}
		total.Sub(total, Range{lo, hi}.Size())
	}
	allocated = new(big.Int)
	for u := range a.used {
		if !a.rng.Contains(u) || a.reserved[u] {
			continue
		}
		if _, ex := a.excluded(u); ex {
			continue
		}
		allocated.Add(allocated, big.NewInt(1))
	}
	return allocated, total.Sub(total, allocated)
}

// Stats returns Counts saturated to int, for api.NetworkStatus.
func (a *Allocator) Stats() (allocated, free int) {
	al, fr := a.Counts()
	return ClampSize(al), ClampSize(fr)
}
