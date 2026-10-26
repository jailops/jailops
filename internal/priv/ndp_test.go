package priv

import (
	"context"
	"net/netip"
	"strings"
	"testing"
	"time"

	"jailops.net/jailops/internal/ndproxy"
	"jailops.net/jailops/sdk/api"
)

func ndpReq() NDPAnswerReq {
	return NDPAnswerReq{Uplink: "em0", Target: "2001:db8:1:2:100::10", RequesterIP: "fe80::1", RequesterMAC: "00:00:5e:00:02:96"}
}

func TestNDPAnswerSendsExactlyTheAdvertisementItIsAskedFor(t *testing.T) {
	e := newEnv(t)
	dev := e.local.d.NDP.(*fakeNDP)
	if err := e.local.NDPAnswer(context.Background(), ndpReq()); err != nil {
		t.Fatal(err)
	}
	if dev.frames() != 1 {
		t.Fatalf("frames sent: %d", dev.frames())
	}
	p, err := ndproxy.Parse(dev.sent[0])
	if err != nil {
		t.Fatal(err)
	}
	me := ndproxy.MAC(dev.hw)
	want := p.Type == ndproxy.TypeNeighborAdvertisement && p.Target == netip.MustParseAddr("2001:db8:1:2:100::10") &&
		p.Src == p.Target && p.Dst == netip.MustParseAddr("fe80::1") && p.EthSrc == me && p.EthDst.String() == "00:00:5e:00:02:96" &&
		p.TargetLLA != nil && *p.TargetLLA == me && p.Flags == ndproxy.FlagSolicited|ndproxy.FlagOverride
	if !want {
		t.Fatalf("the advertisement is not the one that was asked for: %+v", p)
	}
}

func TestNDPAnswerRefusals(t *testing.T) {
	cases := []struct {
		name string
		mod  func(q *NDPAnswerReq, dev *fakeNDP)
		code api.Code
		text string
	}{
		{"an uplink that is not approved", func(q *NDPAnswerReq, _ *fakeNDP) { q.Uplink = "igb0" }, api.CodeForbidden, "ndp-proxy = igb0="},
		{"a bad interface name", func(q *NDPAnswerReq, _ *fakeNDP) { q.Uplink = "-x" }, api.CodeInvalid, ""},
		{"a target outside the approved prefix", func(q *NDPAnswerReq, _ *fakeNDP) { q.Target = "2001:db8:1:2:200::10" }, api.CodeForbidden, "outside the prefixes"},
		{"a target that is the upstream router", func(q *NDPAnswerReq, _ *fakeNDP) { q.Target = "fe80::1" }, api.CodeForbidden, "outside the prefixes"},
		{"a target that is not an address", func(q *NDPAnswerReq, _ *fakeNDP) { q.Target = "nonsense" }, api.CodeInvalid, ""},
		{"an IPv4 target", func(q *NDPAnswerReq, _ *fakeNDP) { q.Target = "10.0.0.1" }, api.CodeInvalid, ""},
		{"a target with a zone", func(q *NDPAnswerReq, _ *fakeNDP) { q.Target = "2001:db8:1:2:100::10%em0" }, api.CodeInvalid, ""},
		{"an address of this host", func(_ *NDPAnswerReq, dev *fakeNDP) { dev.local = []string{"2001:db8:1:2:100::10"} }, api.CodeForbidden, "address of this host"},
		{"duplicate address detection", func(q *NDPAnswerReq, _ *fakeNDP) { q.RequesterIP = "::" }, api.CodeForbidden, "not a unicast neighbor"},
		{"a multicast requester", func(q *NDPAnswerReq, _ *fakeNDP) { q.RequesterIP = "ff02::1" }, api.CodeForbidden, "not a unicast neighbor"},
		{"an IPv4 requester", func(q *NDPAnswerReq, _ *fakeNDP) { q.RequesterIP = "10.0.0.1" }, api.CodeInvalid, ""},
		{"a multicast MAC", func(q *NDPAnswerReq, _ *fakeNDP) { q.RequesterMAC = "33:33:00:00:00:01" }, api.CodeForbidden, "not a unicast"},
		{"a zero MAC", func(q *NDPAnswerReq, _ *fakeNDP) { q.RequesterMAC = "00:00:00:00:00:00" }, api.CodeForbidden, "not a unicast"},
		{"a MAC that is not Ethernet", func(q *NDPAnswerReq, _ *fakeNDP) { q.RequesterMAC = "00:00:5e:00:02:96:01:02" }, api.CodeInvalid, ""},
		{"the host itself as the requester", func(q *NDPAnswerReq, dev *fakeNDP) { q.RequesterMAC = ndproxy.MAC(dev.hw).String() }, api.CodeForbidden, "this host"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			dev := e.local.d.NDP.(*fakeNDP)
			q := ndpReq()
			tc.mod(&q, dev)
			err := e.local.NDPAnswer(context.Background(), q)
			if err == nil || !api.IsCode(err, tc.code) || !strings.Contains(err.Error(), tc.text) {
				t.Fatalf("want %s %q, got %v", tc.code, tc.text, err)
			}
			if dev.frames() != 0 {
				t.Fatal("a frame was sent for a refused request")
			}
		})
	}
}

func TestNDPIsRefusedWithoutAnApproval(t *testing.T) {
	e := newEnv(t)
	cfg := e.cfg
	cfg.NDPProxy = nil
	l, err := NewLocal(cfg, Deps{ZFS: e.zfs, NDP: newFakeNDP(e.base)}, WithoutChown())
	if err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"answer": l.NDPAnswer(context.Background(), ndpReq()),
		"open": func() error {
			f, err := l.NDPOpen(context.Background(), "em0", false)
			if f != nil {
				f.Close()
			}
			return err
		}(),
	} {
		if !api.IsCode(err, api.CodeForbidden) || !strings.Contains(err.Error(), "add the line `ndp-proxy = em0=") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestNDPAnswersAreRateLimited(t *testing.T) {
	e := newEnv(t)
	now := time.Unix(1000, 0)
	e.local.ndp.now = func() time.Time { return now }
	dev := e.local.d.NDP.(*fakeNDP)
	ok, refused := 0, 0
	for i := 0; i < ndpAnswersPerSecond*2; i++ {
		switch err := e.local.NDPAnswer(context.Background(), ndpReq()); {
		case err == nil:
			ok++
		case api.IsCode(err, api.CodeBusy):
			refused++
		default:
			t.Fatal(err)
		}
	}
	if ok != ndpAnswersPerSecond || refused != ndpAnswersPerSecond || dev.frames() != ndpAnswersPerSecond {
		t.Fatalf("ok=%d refused=%d sent=%d", ok, refused, dev.frames())
	}
	now = now.Add(time.Second) // a second later the bucket is full again
	if err := e.local.NDPAnswer(context.Background(), ndpReq()); err != nil {
		t.Fatalf("after a second: %v", err)
	}
}

func TestParseAndNormalizeNDPProxy(t *testing.T) {
	u, err := ParseNDPProxy("em0=2001:db8:1:2:100::/80, 2001:db8:9::/48")
	if err != nil || u.Uplink != "em0" || len(u.Prefixes) != 2 {
		t.Fatalf("%+v %v", u, err)
	}
	for _, bad := range []string{"", "em0", "em0=", "=2001:db8::/64", "em0=nonsense", "em0=10.0.0.0/24"} {
		if u, err := ParseNDPProxy(bad); err == nil {
			if _, err := normalizeNDP([]NDPUplink{u}); err == nil {
				t.Errorf("%q was accepted", bad)
			}
		}
	}
	for name, in := range map[string][]NDPUplink{
		"a prefix that is too wide":      {{Uplink: "em0", Prefixes: []netip.Prefix{netip.MustParsePrefix("2001:db8::/32")}}},
		"a prefix that is a single host": {{Uplink: "em0", Prefixes: []netip.Prefix{netip.MustParsePrefix("2001:db8::1/128")}}},
		"link-local":                     {{Uplink: "em0", Prefixes: []netip.Prefix{netip.MustParsePrefix("fe80::/64")}}},
		"multicast":                      {{Uplink: "em0", Prefixes: []netip.Prefix{netip.MustParsePrefix("ff02::/64")}}},
		"an uplink twice":                {{Uplink: "em0", Prefixes: []netip.Prefix{netip.MustParsePrefix("2001:db8::/64")}}, {Uplink: "em0", Prefixes: []netip.Prefix{netip.MustParsePrefix("2001:db8:1::/64")}}},
		"a bad uplink name":              {{Uplink: "-x", Prefixes: []netip.Prefix{netip.MustParsePrefix("2001:db8::/64")}}},
		"no prefix":                      {{Uplink: "em0"}},
	} {
		if _, err := normalizeNDP(in); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestNDPAnnounceIsOneUnsolicitedAdvertisementToAllNodes(t *testing.T) {
	e := newEnv(t)
	dev := e.local.d.NDP.(*fakeNDP)
	q := NDPAnswerReq{Uplink: "em0", Target: "2001:db8:1:2:100::10", Announce: true}
	if err := e.local.NDPAnswer(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	if dev.frames() != 1 {
		t.Fatalf("frames sent: %d", dev.frames())
	}
	p, err := ndproxy.Parse(dev.sent[0])
	if err != nil {
		t.Fatal(err)
	}
	me := ndproxy.MAC(dev.hw)
	ok := p.Type == ndproxy.TypeNeighborAdvertisement && p.Target == netip.MustParseAddr(q.Target) && p.Src == p.Target &&
		p.Dst == netip.MustParseAddr("ff02::1") && p.EthSrc == me && p.EthDst.String() == "33:33:00:00:00:01" &&
		p.TargetLLA != nil && *p.TargetLLA == me && p.Flags == ndproxy.FlagOverride
	if !ok {
		t.Fatalf("not an unsolicited advertisement of the target to all nodes: %+v", p)
	}
	// the same limits as an answer: approved prefix only, never the host's own address
	for name, mod := range map[string]func(*NDPAnswerReq, *fakeNDP){
		"outside the prefix": func(q *NDPAnswerReq, _ *fakeNDP) { q.Target = "2001:db8:1:2:200::10" },
		"the host itself":    func(_ *NDPAnswerReq, d *fakeNDP) { d.local = []string{"2001:db8:1:2:100::10"} },
		"an unapproved link": func(q *NDPAnswerReq, _ *fakeNDP) { q.Uplink = "igb0" },
	} {
		e := newEnv(t)
		d := e.local.d.NDP.(*fakeNDP)
		q := NDPAnswerReq{Uplink: "em0", Target: "2001:db8:1:2:100::10", Announce: true}
		mod(&q, d)
		if err := e.local.NDPAnswer(context.Background(), q); !api.IsCode(err, api.CodeForbidden) || d.frames() != 0 {
			t.Errorf("%s: %v (frames %d)", name, err, d.frames())
		}
	}
}

// The helper speaks only for addresses that have a host route of a jail on this host: not for the upstream router, not
// for another machine of the segment, even though they lie inside the approved prefix.
func TestNDPOnlyForAddressesThatRouteToAJail(t *testing.T) {
	for name, routes := range map[string]map[string]string{
		"no route at all":         {},
		"a route out the uplink":  {"2001:db8:1:2:100::10": "em0"},
		"a route of someone else": {"2001:db8:1:2:100::10": "tun0"},
	} {
		e := newEnv(t)
		dev := e.local.d.NDP.(*fakeNDP)
		dev.routes = routes
		if err := e.local.NDPAnswer(context.Background(), ndpReq()); !api.IsCode(err, api.CodeForbidden) {
			t.Errorf("%s (answer): %v", name, err)
		}
		a := NDPAnswerReq{Uplink: "em0", Target: "2001:db8:1:2:100::10", Announce: true}
		if err := e.local.NDPAnswer(context.Background(), a); !api.IsCode(err, api.CodeForbidden) {
			t.Errorf("%s (announce): %v", name, err)
		}
		if dev.frames() != 0 {
			t.Errorf("%s: a frame was sent", name)
		}
	}
	// a route that appears is noticed within the cache's age
	e := newEnv(t)
	dev := e.local.d.NDP.(*fakeNDP)
	now := time.Unix(5000, 0)
	e.local.ndp.now = func() time.Time { return now }
	dev.routes = map[string]string{}
	if err := e.local.NDPAnswer(context.Background(), ndpReq()); err == nil {
		t.Fatal("answered without a route")
	}
	dev.routes = map[string]string{"2001:db8:1:2:100::10": "jo30gb"}
	now = now.Add(2 * ndpRoutesMaxAge)
	if err := e.local.NDPAnswer(context.Background(), ndpReq()); err != nil {
		t.Fatalf("after the route appeared: %v", err)
	}
}

// A route installed a moment ago is found at once when the reading that lacks it is older than the miss age, and the
// table is not read again for each request within it.
func TestNDPRereadsTheRoutesOnAMissAfterTheMissAge(t *testing.T) {
	e := newEnv(t)
	dev := e.local.d.NDP.(*fakeNDP)
	now := time.Unix(7000, 0)
	e.local.ndp.now = func() time.Time { return now }
	dev.routes = map[string]string{}
	a := NDPAnswerReq{Uplink: "em0", Target: "2001:db8:1:2:100::10", Announce: true}
	if err := e.local.NDPAnswer(context.Background(), a); err == nil {
		t.Fatal("announced without a route")
	}
	dev.routes = map[string]string{"2001:db8:1:2:100::10": "jo30gb"}
	now = now.Add(ndpRoutesMissAge / 2)
	if err := e.local.NDPAnswer(context.Background(), a); err == nil {
		t.Fatal("the table was read again inside the miss age")
	}
	now = now.Add(ndpRoutesMissAge)
	if err := e.local.NDPAnswer(context.Background(), a); err != nil {
		t.Fatalf("a route younger than the cache's age was refused: %v", err)
	}
}

func TestParseHostRoutes(t *testing.T) {
	text := `Routing tables

Internet6:
Destination                       Gateway                       Flags         Netif Expire
default                           fe80::1%vtnet0                UGS          vtnet0
2a0a:4cc0:c1:3bb8::1              link#2                        UHS             lo0
2a0a:4cc0:c1:3bb8:100::10         link#8                        UHS          jo30gb
fe80::%vtnet0/64                  link#1                        U            vtnet0
fe80::1%lo0                       link#11                       UHS             lo0
`
	r := parseHostRoutes(text)
	if r[netip.MustParseAddr("2a0a:4cc0:c1:3bb8:100::10")] != "jo30gb" || len(r) != 2 {
		t.Fatalf("%v", r)
	}
}

func TestParseStaticNeighbors(t *testing.T) {
	text := `Neighbor                             Linklayer Address  Netif Expire    S Flags
fe80::1%jo30gb                       58:9c:fc:10:5e:2c jo30gb permanent R 
fe80::e85c:a9ff:fed2:4e32%jo30gb     ea:5c:a9:d2:4e:32 jo30gb permanent R 
2a0a:4cc0:c1:3bb8:100::10            4e:10:c9:7c:3e:00 jo30gb permanent R 
fe80::d4b8:89ff:fe47:7d4%vtnet0      d6:b8:89:47:07:d4 vtnet0 23h50m S R
`
	got := parseStaticNeighbors(text)
	if len(got) != 2 || got[1].Addr.String() != "fe80::e85c:a9ff:fed2:4e32" || got[1].Iface != "jo30gb" || got[1].MAC != "ea:5c:a9:d2:4e:32" {
		t.Fatalf("%+v", got)
	}
}

func TestParseStaticARP(t *testing.T) {
	text := `? (10.94.0.1) at 58:9c:fc:10:5e:2c on jo0gb permanent [ethernet]
? (10.94.0.2) at 4e:10:c9:7c:3e:00 on jo0gb permanent [ethernet]
? (10.94.0.3) at ea:5c:a9:d2:4e:32 on jo0gb expires in 1168 seconds [ethernet]
`
	got := parseStaticARP(text)
	if len(got) != 2 || got[1].Addr.String() != "10.94.0.2" || got[1].Iface != "jo0gb" || got[1].MAC != "4e:10:c9:7c:3e:00" {
		t.Fatalf("%+v", got)
	}
}
