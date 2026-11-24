package ndproxy

import (
	"net/netip"
	"testing"
	"time"
)

var (
	hostAddr = netip.MustParseAddr("3fff:40:12c::1") // the host's own /128 on the uplink, outside the proxied prefix
	backup   = netip.MustParseAddr("3fff:40::3")     // the VRRP backup router of the provider
	vrrpMAC  = MAC{0x00, 0x00, 0x5e, 0x00, 0x02, 0x01}
)

// the backup router asks for the host's address with a unicast solicitation from its global address, and the host
// answers through its default route, to the MAC of the gateway
func (r *rig) askHost(answerTo MAC) {
	r.p.HandleFrame(BuildNS(macB, macH, backup, hostAddr, hostAddr, &macB))
	r.p.HandleFrame(BuildNA(macH, answerTo, hostAddr, backup, hostAddr, FlagSolicited|FlagOverride, &macH))
}

func TestReplyPathSeesTheHostAnswerTheGatewayInsteadOfTheRouter(t *testing.T) {
	r := newRig(t, Config{})
	for i := 0; i < 3; i++ {
		r.askHost(vrrpMAC)
		r.clk.Advance(time.Second)
	}
	got, seen := r.p.ReplyPaths()
	if seen != 3 || len(got) != 1 {
		t.Fatalf("seen %d, %+v", seen, got)
	}
	rp := got[0]
	if rp.Router != backup || rp.Host != hostAddr || rp.RouterMAC != macB || rp.NADst != vrrpMAC || !rp.Misdirected || rp.Repeats != 1 {
		t.Fatalf("%+v", rp)
	}
	if len(r.inj.all()) != 0 {
		t.Fatal("the proxy answered for the host's own address")
	}
	// the status carries it
	if st := r.p.Status(); len(st.ReplyPath) != 1 || st.HostSolicits != 3 {
		t.Fatalf("%+v", st)
	}
}

func TestReplyPathAnswerToTheRouterIsNotMisdirected(t *testing.T) {
	r := newRig(t, Config{})
	for i := 0; i < 4; i++ {
		r.askHost(macB)
		r.clk.Advance(time.Second)
	}
	got, _ := r.p.ReplyPaths()
	if len(got) != 1 || got[0].Misdirected || got[0].NADst != macB || got[0].Repeats != 2 {
		t.Fatalf("%+v", got)
	}
}

func TestReplyPathNeedsRepeatsAboutASecondApart(t *testing.T) {
	r := newRig(t, Config{})
	// two solicitations: the router got its answer
	r.askHost(vrrpMAC)
	r.clk.Advance(time.Second)
	r.askHost(vrrpMAC)
	// a burst within a few milliseconds is one solicitation
	for i := 0; i < 5; i++ {
		r.clk.Advance(10 * time.Millisecond)
		r.askHost(vrrpMAC)
	}
	if got, seen := r.p.ReplyPaths(); len(got) != 0 || seen != 7 {
		t.Fatalf("seen %d, %+v", seen, got)
	}
	// solicitations further apart than the window are no repeat either
	r.clk.Advance(6 * time.Second)
	r.askHost(vrrpMAC)
	r.clk.Advance(3 * time.Second)
	r.askHost(vrrpMAC)
	r.clk.Advance(3 * time.Second)
	r.askHost(vrrpMAC)
	if got, _ := r.p.ReplyPaths(); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}

// The addresses of the jails are answered by the proxy itself, to the asker's MAC: repeated solicitations for them are
// never a reply path of the host, and neither is one from a link-local source or one sent to another host's MAC.
func TestReplyPathLeavesProxiedAddressesAndOthersAlone(t *testing.T) {
	r := newRig(t, Config{})
	r.add(t, jailA)
	other := MAC{2, 0, 0, 0, 0, 0x77}
	for i := 0; i < 3; i++ {
		r.p.HandleFrame(BuildNS(macB, macH, backup, jailA, jailA, &macB))
		// the proxy's own advertisement, as the capture sees it going out
		r.p.HandleFrame(BuildNA(macH, vrrpMAC, jailA, backup, jailA, FlagSolicited|FlagOverride, &macH))
		// the router's multicast solicitation from its link-local address
		r.p.HandleFrame(mkNS(router, hostAddr, SolicitedNode(hostAddr), macB, &macB))
		// another host of the segment, asked for its own address
		r.p.HandleFrame(BuildNS(macB, other, backup, netip.MustParseAddr("3fff:40:12d::1"), netip.MustParseAddr("3fff:40:12d::1"), &macB))
		r.clk.Advance(time.Second)
	}
	if got, seen := r.p.ReplyPaths(); len(got) != 0 || seen != 0 {
		t.Fatalf("seen %d, %+v", seen, got)
	}
	if n := len(r.inj.all()); n != 3 {
		t.Fatalf("the proxy answered %d of the 3 solicitations for the jail", n)
	}
}
