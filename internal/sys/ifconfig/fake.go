package ifconfig

import (
	"context"
	"net/netip"
	"sort"
	"strconv"
	"sync"

	"jailops.net/jailops/sdk/api"
)

type fakeIf struct {
	Iface
	jail string // non-empty: lives in that jail's vnet
	peer string // epair peer
}

// Fake is an in-memory interface table with the same name rules as Command.
type Fake struct {
	mu         sync.Mutex
	o          Options
	ifs        map[string]*fakeIf
	routes     map[string]bool
	neighbors  map[string]string
	nextBridge int
	nextEpair  int
	// Forwarding is the value IPv6Forwarding returns.
	Forwarding bool
}

var _ Client = (*Fake)(nil)

// NewFake returns a Fake with a loopback interface.
func NewFake(o Options) *Fake {
	f := &Fake{o: o, ifs: map[string]*fakeIf{}, routes: map[string]bool{}, neighbors: map[string]string{}}
	f.ifs["lo0"] = &fakeIf{Iface: Iface{Name: "lo0", Flags: []string{"UP", "LOOPBACK", "RUNNING"}, MTU: 16384}}
	return f
}

// AddForeign inserts an interface JailOps does not own (test helper).
func (f *Fake) AddForeign(name string, up bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := &fakeIf{Iface: Iface{Name: name, MTU: 1500}}
	if up {
		i.Flags = []string{"UP"}
	}
	f.ifs[name] = i
}

// Routes returns the installed routes as "-inet dest gw" strings.
func (f *Fake) Routes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var r []string
	for k := range f.routes {
		r = append(r, k)
	}
	sort.Strings(r)
	return r
}

func notFound(n string) error {
	return api.Errorf(api.CodeNotFound, "ifconfig: interface %s does not exist", n)
}

// get returns a host-visible interface.
func (f *Fake) get(n string) (*fakeIf, error) {
	i := f.ifs[n]
	if i == nil || i.jail != "" {
		return nil, notFound(n)
	}
	return i, nil
}

// List implements Client. Interfaces moved into a jail are not listed.
func (f *Fake) List(context.Context) ([]Iface, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Iface
	for _, i := range f.ifs {
		if i.jail == "" {
			c := i.Iface
			c.Members = append([]Member(nil), c.Members...)
			c.Inet = append([]netip.Prefix(nil), c.Inet...)
			c.Inet6 = append([]netip.Prefix(nil), c.Inet6...)
			out = append(out, c)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out, nil
}

// BridgeCreate implements Client.
func (f *Fake) BridgeCreate(_ context.Context, name string) error {
	if err := f.o.owned(name); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ifs[name] != nil {
		return api.Errorf(api.CodeAlreadyExists, "ifconfig: interface %s already exists", name)
	}
	f.ifs[name] = &fakeIf{Iface: Iface{Name: name, MTU: 1500, Flags: []string{"BROADCAST", "MULTICAST"}, Groups: []string{"bridge"}}}
	return nil
}

// BridgeDestroy implements Client.
func (f *Fake) BridgeDestroy(_ context.Context, name string) error {
	if err := f.o.owned(name); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.get(name); err != nil {
		return err
	}
	delete(f.ifs, name)
	return nil
}

// BridgeAddMember implements Client.
func (f *Fake) BridgeAddMember(_ context.Context, bridge, member string, private bool) error {
	if err := f.o.owned(bridge); err != nil {
		return err
	}
	if err := ValidateIfName(member); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	b, err := f.get(bridge)
	if err != nil {
		return err
	}
	if _, err := f.get(member); err != nil {
		return err
	}
	for _, m := range b.Members {
		if m.Name == member {
			return api.Errorf(api.CodeAlreadyExists, "ifconfig: %s is already a member of %s", member, bridge)
		}
	}
	m := Member{Name: member, Flags: []string{"LEARNING", "DISCOVER", "AUTOEGRESS"}, Private: private}
	if private {
		m.Flags = append(m.Flags, "PRIVATE")
	}
	b.Members = append(b.Members, m)
	return nil
}

// BridgeDeleteMember implements Client.
func (f *Fake) BridgeDeleteMember(_ context.Context, bridge, member string) error {
	if err := f.o.owned(bridge); err != nil {
		return err
	}
	if err := ValidateIfName(member); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	b, err := f.get(bridge)
	if err != nil {
		return err
	}
	for k, m := range b.Members {
		if m.Name == member {
			b.Members = append(b.Members[:k], b.Members[k+1:]...)
			return nil
		}
	}
	return api.Errorf(api.CodeNotFound, "ifconfig: %s is not a member of %s", member, bridge)
}

// SetDescription implements Client.
func (f *Fake) SetDescription(_ context.Context, iface, desc string) error {
	if err := f.o.owned(iface); err != nil {
		return err
	}
	if err := ValidateDescription(desc); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	i, err := f.get(iface)
	if err != nil {
		return err
	}
	i.Description = desc
	return nil
}

// EpairCreate implements Client.
func (f *Fake) EpairCreate(_ context.Context, name string) (string, string, error) {
	a, b := name+"a", name+"b"
	if err := f.o.owned(a); err != nil {
		return "", "", err
	}
	if err := ValidateIfName(b); err != nil {
		return "", "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ifs[a] != nil || f.ifs[b] != nil {
		return "", "", api.Errorf(api.CodeAlreadyExists, "ifconfig: interface %s already exists", a)
	}
	f.nextEpair++
	for _, n := range []string{a, b} {
		f.ifs[n] = &fakeIf{Iface: Iface{Name: n, MTU: 1500, Flags: []string{"BROADCAST", "MULTICAST"}, Groups: []string{"epair"},
			MAC: "02:00:00:00:00:" + strconv.FormatInt(int64(0x10+f.nextEpair), 16)}}
	}
	f.ifs[a].peer, f.ifs[b].peer = b, a
	return a, b, nil
}

// EpairDestroy implements Client.
func (f *Fake) EpairDestroy(_ context.Context, iface string) error {
	if err := f.o.owned(iface); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	i, err := f.get(iface)
	if err != nil {
		return err
	}
	if i.peer == "" {
		return api.Errorf(api.CodeInvalid, "ifconfig: %s is not an epair", iface)
	}
	f.removeLocked(i.peer)
	f.removeLocked(iface)
	return nil
}

func (f *Fake) removeLocked(n string) {
	delete(f.ifs, n)
	for _, i := range f.ifs {
		for k, m := range i.Members {
			if m.Name == n {
				i.Members = append(i.Members[:k], i.Members[k+1:]...)
				break
			}
		}
	}
}

// MoveToJail implements Client. The interface leaves its bridge, as it does
// when it changes vnet.
func (f *Fake) MoveToJail(_ context.Context, iface, jail string) error {
	if err := f.o.owned(iface); err != nil {
		return err
	}
	if err := ValidateJailRef(jail); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	i, err := f.get(iface)
	if err != nil {
		return err
	}
	for _, b := range f.ifs {
		for k, m := range b.Members {
			if m.Name == iface {
				b.Members = append(b.Members[:k], b.Members[k+1:]...)
				break
			}
		}
	}
	i.jail = jail
	return nil
}

// MoveFromJail implements Client.
func (f *Fake) MoveFromJail(_ context.Context, iface, jail string) error {
	if err := f.o.owned(iface); err != nil {
		return err
	}
	if err := ValidateJailRef(jail); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.ifs[iface]
	if i == nil || i.jail != jail {
		return notFound(iface)
	}
	i.jail = ""
	return nil
}

// SetLink implements Client.
func (f *Fake) SetLink(_ context.Context, iface string, up bool) error {
	if err := f.o.owned(iface); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	i, err := f.get(iface)
	if err != nil {
		return err
	}
	var fl []string
	for _, x := range i.Flags {
		if x != "UP" {
			fl = append(fl, x)
		}
	}
	if up {
		fl = append([]string{"UP"}, fl...)
	}
	i.Flags = fl
	return nil
}

func (f *Fake) addr(iface string, p netip.Prefix, add bool) error {
	if err := f.o.owned(iface); err != nil {
		return err
	}
	if err := validPrefix(p); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	i, err := f.get(iface)
	if err != nil {
		return err
	}
	l := &i.Inet
	if p.Addr().Is6() {
		l = &i.Inet6
	}
	for k, x := range *l {
		if x == p {
			if add {
				return api.Errorf(api.CodeAlreadyExists, "ifconfig: address %s already assigned", p)
			}
			*l = append((*l)[:k], (*l)[k+1:]...)
			return nil
		}
	}
	if add {
		*l = append(*l, p)
		return nil
	}
	return api.Errorf(api.CodeNotFound, "ifconfig: address %s not assigned", p)
}

// AddrAdd implements Client.
func (f *Fake) AddrAdd(_ context.Context, iface string, p netip.Prefix) error {
	return f.addr(iface, p, true)
}

// AddrRemove implements Client.
func (f *Fake) AddrRemove(_ context.Context, iface string, p netip.Prefix) error {
	return f.addr(iface, p, false)
}

func (f *Fake) route(r Route, add bool) error {
	a, err := routeArgs(f.o, r)
	if err != nil {
		return err
	}
	key := ""
	for _, x := range a {
		key += x + " "
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if add {
		if f.routes[key] {
			return api.Errorf(api.CodeAlreadyExists, "route: File exists")
		}
		f.routes[key] = true
		return nil
	}
	if !f.routes[key] {
		return api.Errorf(api.CodeNotFound, "route: not in table")
	}
	delete(f.routes, key)
	return nil
}

// RouteAdd implements Client.
func (f *Fake) RouteAdd(_ context.Context, r Route) error { return f.route(r, true) }

// RouteDelete implements Client.
func (f *Fake) RouteDelete(_ context.Context, r Route) error { return f.route(r, false) }

// Neighbors returns the static neighbor entries as "iface addr" to MAC.
func (f *Fake) Neighbors() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]string{}
	for k, v := range f.neighbors {
		out[k] = v
	}
	return out
}

// NeighborSet implements Client.
func (f *Fake) NeighborSet(_ context.Context, iface string, a netip.Addr, mac string) error {
	if err := f.o.owned(iface); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.neighbors[iface+" "+a.String()] = mac
	return nil
}

// NeighborDelete implements Client.
func (f *Fake) NeighborDelete(_ context.Context, iface string, a netip.Addr) error {
	if err := f.o.owned(iface); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	k := iface + " " + a.String()
	if _, ok := f.neighbors[k]; !ok {
		return api.Errorf(api.CodeNotFound, "ndp: no entry for %s", a)
	}
	delete(f.neighbors, k)
	return nil
}

// IPv6Forwarding implements Client.
func (f *Fake) IPv6Forwarding(context.Context) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Forwarding, nil
}
