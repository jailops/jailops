package ndproxy

import (
	"net/netip"
	"sort"
	"sync"
	"time"

	"jailops.net/jailops/sdk/api"
)

// maxEntries bounds the proxied addresses of one proxy.
const maxEntries = 4096

// Clock abstracts time so tests are deterministic.
type Clock interface {
	Now() time.Time
	// After returns a channel that receives once after d.
	After(d time.Duration) <-chan time.Time
}

// Requester identifies who asked: the IPv6 source and the link-layer
// address an advertisement must be sent to.
type Requester struct {
	IP  netip.Addr
	MAC MAC
}

// Table holds the proxied addresses: the proxy answers a solicitation at once for every address in it. JailOps
// fills it with the allocation range of the network and the addresses of the running jails. It is safe for
// concurrent use and does no I/O.
type Table struct {
	mu      sync.Mutex
	entries map[netip.Addr]struct{}
}

// NewTable returns an empty Table.
func NewTable() *Table {
	return &Table{entries: map[netip.Addr]struct{}{}}
}

// ValidAddr reports whether a may be proxied: a global or unique-local
// unicast address (not link-local, loopback, multicast or unspecified).
func ValidAddr(a netip.Addr) bool {
	return a.Is6() && !a.Is4In6() && !a.IsUnspecified() && !a.IsLoopback() && !a.IsMulticast() &&
		!a.IsLinkLocalUnicast() && a.Zone() == ""
}

// Add starts proxying addr.
func (t *Table) Add(addr netip.Addr) error {
	if !ValidAddr(addr) {
		return api.Errorf(api.CodeInvalid, "address %s cannot be proxied", addr)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.entries[addr]; ok {
		return api.Errorf(api.CodeAlreadyExists, "address %s is already proxied", addr)
	}
	if len(t.entries) >= maxEntries {
		return api.Errorf(api.CodePoolExhausted, "at most %d proxied addresses", maxEntries)
	}
	t.entries[addr] = struct{}{}
	return nil
}

// Remove stops proxying addr. It reports whether the address was present.
func (t *Table) Remove(addr netip.Addr) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, ok := t.entries[addr]
	delete(t.entries, addr)
	return ok
}

// Has reports whether addr is proxied.
func (t *Table) Has(addr netip.Addr) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, ok := t.entries[addr]
	return ok
}

// Len returns the number of proxied addresses.
func (t *Table) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.entries)
}

// Addrs returns the proxied addresses in order.
func (t *Table) Addrs() []netip.Addr {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]netip.Addr, 0, len(t.entries))
	for a := range t.entries {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Less(out[j]) })
	return out
}
