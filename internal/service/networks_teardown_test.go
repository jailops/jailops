package service

import (
	"jailops.net/jailops/internal/sys/ifconfig"
	"path/filepath"
	"strings"
	"testing"

	"jailops.net/jailops/internal/render"
	"jailops.net/jailops/internal/state"
	"jailops.net/jailops/internal/store"
	"jailops.net/jailops/internal/sys/pfctl"
	"jailops.net/jailops/sdk/api"
)

// Every network has an anchor (the pass of its gateway port), not only the ones with NAT; deleting the network takes
// it away so nothing is left that names an interface another network can be given later.
func TestDeletingANetworkFlushesItsAnchorWithoutNAT(t *testing.T) {
	st, err := state.Open(t.TempDir(), state.Options{})
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	pf := pfctl.NewFake()
	s, err := New(Deps{State: st, DB: db, JournalDir: t.TempDir(), HostUUID: "h", PF: pf})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(); db.Close(); st.Close() })

	anchor := render.NetAnchor("plain")
	if err := pf.Load(bg, anchor, "pass quick on jo21ga all no state\n"); err != nil {
		t.Fatal(err)
	}
	s.teardownNetwork(bg, object[api.NetworkSpec]{
		Meta: api.ObjectMeta{Name: "plain"},
		Spec: api.NetworkSpec{Bridge: "jo21"},
	})
	if got, _ := pf.Read(bg, anchor); got != "" {
		t.Errorf("the anchor of the deleted network is still loaded:\n%s", got)
	}
}

func TestDoctorReportsAProxyNetworkThatIsNotAnswering(t *testing.T) {
	s := newSvc(t)
	spec := api.NetworkSpec{Bridge: "jo40", Uplink: "vtnet0", IPv6: &api.IPv6Net{Mode: api.IPv6Proxy, Subnet: "2001:db8:1:2:100::/80"}}
	if _, err := s.netKind.create(bg, api.ObjectMeta{Name: "sw"}, spec); err != nil {
		t.Fatalf("cannot create the network: %v", err)
	}
	c := &checkSet{}
	s.doctorNDPProxy(c)
	if len(c.list) != 1 || c.list[0].Status != api.DoctorFail || c.list[0].Hint == "" {
		t.Fatalf("%+v", c.list)
	}
	s.NetworkStatus("sw", api.NetworkStatus{Conditions: []api.Condition{{Type: "NDPProxy", Status: api.ConditionTrue}}})
	c = &checkSet{}
	s.doctorNDPProxy(c)
	if len(c.list) != 1 || c.list[0].Status != api.DoctorOK {
		t.Fatalf("%+v", c.list)
	}
	// A capture that is still being opened (the daemon just started) is a warning, not a failure.
	s.NetworkStatus("sw", api.NetworkStatus{Conditions: []api.Condition{{Type: "NDPProxy", Status: api.ConditionFalse, Reason: "Starting", Message: "the capture of vtnet0 is being opened"}}})
	c = &checkSet{}
	s.doctorNDPProxy(c)
	if len(c.list) != 1 || c.list[0].Status != api.DoctorWarn || !strings.Contains(c.list[0].Message, "the capture of vtnet0 is being opened") ||
		!strings.Contains(c.list[0].Message, "within a few minutes after the daemon starts") {
		t.Fatalf("%+v", c.list)
	}
	// A capture that cannot be opened stays a failure.
	for _, reason := range []string{"NoCapture", "SyncFailed", "Stopped"} {
		s.NetworkStatus("sw", api.NetworkStatus{Conditions: []api.Condition{{Type: "NDPProxy", Status: api.ConditionFalse, Reason: reason, Message: "x"}}})
		c = &checkSet{}
		s.doctorNDPProxy(c)
		if len(c.list) != 1 || c.list[0].Status != api.DoctorFail {
			t.Fatalf("%s: %+v", reason, c.list)
		}
	}
}

func TestNetworksWithOverlappingSubnetsAreRefused(t *testing.T) {
	v4 := func(subnet, gw, rng string) api.NetworkSpec {
		return api.NetworkSpec{IPv4: &api.IPv4Net{Subnet: subnet, Gateway: gw, Range: rng}}
	}
	v6 := func(subnet, gw, rng string) api.NetworkSpec {
		return api.NetworkSpec{IPv6: &api.IPv6Net{Mode: api.IPv6Routed, Subnet: subnet, Gateway: gw, Range: rng}}
	}
	base := []api.NetworkSpec{
		v4("10.20.0.0/24", "10.20.0.1", "10.20.0.2-10.20.0.9"),
		v6("2001:db8:1:2:1::/80", "2001:db8:1:2:1::1", "2001:db8:1:2:1::10-2001:db8:1:2:1::20"),
	}
	s := &Service{}
	_ = s
	for _, c := range []struct {
		name string
		a, b api.NetworkSpec
		want bool
	}{
		{"same v4", base[0], v4("10.20.0.0/24", "10.20.0.1", "10.20.0.2-10.20.0.9"), true},
		{"v4 inside", base[0], v4("10.20.0.128/25", "10.20.0.129", "10.20.0.130-10.20.0.140"), true},
		{"v4 containing", base[0], v4("10.20.0.0/16", "10.20.0.1", "10.20.0.2-10.20.0.9"), true},
		{"v4 apart", base[0], v4("10.21.0.0/24", "10.21.0.1", "10.21.0.2-10.21.0.9"), false},
		{"v6 inside", base[1], v6("2001:db8:1:2:1::/96", "2001:db8:1:2:1::1", "2001:db8:1:2:1::10-2001:db8:1:2:1::20"), true},
		{"v6 apart", base[1], v6("2001:db8:1:2:2::/80", "2001:db8:1:2:2::1", "2001:db8:1:2:2::10-2001:db8:1:2:2::20"), false},
		{"v4 against v6", base[0], base[1], false},
	} {
		if _, _, got := overlappingSubnets(c.a, c.b); got != c.want {
			t.Errorf("%s: overlap=%v, want %v", c.name, got, c.want)
		}
	}
}

func TestUplinkOfTheProxy(t *testing.T) {
	eth := func(n string, groups ...string) ifconfig.Iface { return ifconfig.Iface{Name: n, Groups: groups} }
	ifs := []ifconfig.Iface{
		eth("vtnet0"), eth("em1"),
		{Name: "vlan7", Groups: []string{"vlan"}, VlanParent: "em1", VlanTag: 7},
		{Name: "jo1", Groups: []string{"bridge"}, Members: []ifconfig.Member{{Name: "em2"}}}, eth("em2"),
		eth("lo0", "lo"), eth("jo1ga", "epair"),
	}
	for up, want := range map[string]string{
		"vtnet0": "", "vlan7": "",
		"em1":   "parent of vlan interface(s) vlan7",
		"em2":   "member of bridge jo1",
		"lo0":   "lo interface",
		"jo1ga": "epair interface",
		"nope0": "does not exist",
	} {
		got := uplinkProblem(up, ifs)
		if (want == "") != (got == "") || !strings.Contains(got, want) {
			t.Errorf("%s: %q, want %q", up, got, want)
		}
	}
}

func TestScrubDetection(t *testing.T) {
	if !hasScrub("scrub in all fragment reassemble\nblock drop log all\n") || !hasScrub("match in all scrub (max-mss 1440)\n") {
		t.Error("scrub not found")
	}
	if hasScrub("pass all flags S/SA keep state\nanchor \"jailops/*\" all\n") {
		t.Error("scrub found where there is none")
	}
}

func TestGatewaysWithoutStaticARP(t *testing.T) {
	ifs := []ifconfig.Iface{
		{Name: "jo0gb", Flags: []string{"UP", "STATICARP"}},
		{Name: "jo1gb", Flags: []string{"UP"}},
	}
	got := gatewaysWithoutStaticARP(ifs, []string{"jo0", "jo1", "jo2"})
	if len(got) != 1 || got[0] != "jo1gb" {
		t.Fatalf("%v", got)
	}
}

// A Linux image build in progress counts like a Linux jail for the host pass: the first build needs the Linux modules
// before any jail exists, and the state goes away again when the build is over.
func TestALinuxImageBuildAssertsTheLinuxModules(t *testing.T) {
	if got := hostModules(true); len(got) < 5 || got[3] != "linux64" {
		t.Fatalf("%v", got)
	}
	st, err := state.Open(t.TempDir(), state.Options{})
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Deps{State: st, DB: db, JournalDir: t.TempDir(), HostUUID: "h", PF: pfctl.NewFake()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(); db.Close(); st.Close() })
	if s.hasLinuxJail() {
		t.Fatal("a host without a Linux jail or build needs no Linux modules")
	}
	if _, err := s.imgKind.create(bg, api.ObjectMeta{Name: "alpine"}, api.ImageSpec{OS: api.OSLinux, Release: "3.22", Arch: "arm64", Source: api.ImageSource{Type: "minirootfs"}}); err != nil {
		t.Fatal(err)
	}
	if s.hasLinuxJail() {
		t.Fatal("a defined Linux image alone must not keep the Linux state on the host")
	}
	s.linuxBuilds.Add(1)
	if !s.hasLinuxJail() || len(s.hostDesired().Modules) < 5 || s.hostDesired().Sysctls[SysctlFallbackBrand] != fallbackBrandLinux {
		t.Fatalf("a Linux build in progress does not make the host pass load the Linux modules: %+v", s.hostDesired())
	}
	s.linuxBuilds.Add(-1)
	if s.hasLinuxJail() {
		t.Fatal("the Linux state must go away when the build is over")
	}
}

func TestNetworksWithoutDNS(t *testing.T) {
	got := networksWithoutDNS(map[string][]string{"a": nil, "b": {"1.1.1.1"}, "c": nil, "d": nil}, map[string]bool{"a": true, "b": true, "c": true})
	if len(got) != 2 || got[0] != "a" || got[1] != "c" {
		t.Fatalf("%v", got)
	}
}
