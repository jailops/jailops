package ipam

import (
	"crypto/rand"
	"math/big"
	"net/netip"
	"sort"

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
	random   bool    // a new address is drawn at random within the range
	excl     []Range // merged
	reserved map[netip.Addr]bool
	used     map[netip.Addr]string
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
		// fdff:ffff:ffff:ffff, the form the RFC writes for EUI-64 interfaces), whenever they lie inside the prefix (a /64,
		// or a longer prefix at the end of its /64: the NDP proxy hands out /80s). A prefix shorter than a /64 has no
		// such rule.
		last := lastOf(prefix)
		a.reserved[last] = true
		if prefix.Bits() >= 64 {
			// the 128 identifiers are the top of the /64 that holds the prefix: a /80 at the end of its /64 has them
			// inside it, and a prefix elsewhere in the /64 has none
			last64 := lastOf(netip.PrefixFrom(prefix.Addr(), 64))
			for i := 0; i < 128; i++ {
				c := last64.As16()
				c[15] = byte(0x80 + i)
				if x := netip.AddrFrom16(c); prefix.Contains(x) {
					a.reserved[x] = true
				}
				c[8] &^= 0x02
				if x := netip.AddrFrom16(c); prefix.Contains(x) {
					a.reserved[x] = true
				}
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
	if n == nil {
		return nil, invalid("ipv4", "network has no IPv4 configuration")
	}
	return fromStrings("ipv4", n.Subnet, n.Gateway, n.Range, n.Exclude, inUse)
}

// NewV6 builds an Allocator from the network's IPv6 configuration.
func NewV6(n *api.IPv6Net, inUse []netip.Addr) (*Allocator, error) {
	if n == nil {
		return nil, invalid("ipv6", "network has no IPv6 configuration")
	}
	gw := ""
	if p, err := netip.ParsePrefix(n.Subnet); err == nil {
		if r := Resolver6(p); r.IsValid() {
			gw = r.String() // the host's own address on the network: kept back from the jails
		}
	}
	al, err := fromStrings("ipv6", n.Subnet, gw, n.Range, n.Exclude, inUse)
	if err != nil {
		return nil, err
	}
	al.random = n.Assignment == api.AssignRandom
	return al, nil
}

func fromStrings(field, subnet, gw, rng string, excl []string, inUse []netip.Addr) (*Allocator, error) {
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
	return New(p, g, r, ex, inUse)
}

// Overlap reports whether two ranges of one family share an address.
func Overlap(a, b Range) bool {
	if a.First.BitLen() != b.First.BitLen() {
		return false
	}
	return !a.Last.Less(b.First) && !b.Last.Less(a.First)
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
		return a.take(norm(preferred), owner)
	}
	if a.random {
		if x, ok := a.draw(owner); ok {
			return x, nil
		}
		// a range that is nearly full: the lowest free address is found by the walk below
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

// draw takes a free address chosen at random within the whole prefix of the network, for a network whose jails get random
// addresses: the bits below the prefix are random, as the interface identifier an address would otherwise get from a MAC
// address. It tries a few hundred draws: in a prefix with any room at all one of them is free, and a prefix that is nearly full
// is left to the walk of Allocate (which keeps to the allocation range).
func (a *Allocator) draw(owner string) (netip.Addr, bool) {
	base := a.prefix.Masked().Addr().As16()
	bits := a.prefix.Bits()
	for try := 0; try < 256; try++ {
		var rnd [16]byte
		if _, err := rand.Read(rnd[:]); err != nil {
			return netip.Addr{}, false
		}
		var raw [16]byte
		for i := 0; i < 16; i++ {
			keep := 0 // the bits of this byte that belong to the prefix
			if bits >= (i+1)*8 {
				keep = 8
			} else if bits > i*8 {
				keep = bits - i*8
			}
			mask := byte(0xff << (8 - keep))
			raw[i] = base[i]&mask | rnd[i]&^mask
		}
		cand := netip.AddrFrom16(raw)
		if a.reserved[cand] {
			continue
		}
		if _, ok := a.excluded(cand); ok {
			continue
		}
		if _, taken := a.used[cand]; taken {
			continue
		}
		a.used[cand] = owner
		return cand, true
	}
	return netip.Addr{}, false
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
