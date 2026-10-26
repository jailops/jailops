package ndp

import (
	"fmt"
	"net/netip"
)

// Tally counts what a listener saw for one jail address in one phase.
type Tally struct {
	McastNS int // NS sent to a multicast destination (address resolution)
	UcastNS int // NS sent to a unicast destination (NUD probe)
	NA      int // NA for the target
	HostNS  int // NS for the target whose Ethernet source is the host (host asking for the jail address)
	OutNA   int // NA for the target sent by the host MAC (answers, seen because outbound frames are tapped)
	Other   int
}

// Add classifies a frame.
func (t *Tally) Add(f *Frame, target netip.Addr, hostMAC MAC) {
	if f == nil || f.Target != target || !(f.IsNS() || f.IsNA()) {
		t.Other++
		return
	}
	switch {
	case f.IsNS() && f.EthSrc == hostMAC:
		t.HostNS++
	case f.MulticastNS():
		t.McastNS++
	case f.UnicastNS():
		t.UcastNS++
	case f.IsNA():
		t.NA++
		if f.EthSrc == hostMAC {
			t.OutNA++
		}
	}
}

// ComboResult is one cell of the Part B matrix.
type ComboResult struct {
	Promisc, Joined bool
	PromiscFlag     bool // ifconfig showed PROMISC on the uplink while the combo ran
	McastRan        bool
	UcastRan        bool
	Mcast           Tally // phase with multicast stimulus
	Ucast           Tally // phase with unicast (NUD) stimulus
}

// Outcome of a hypothesis.
type Outcome string

const (
	Confirmed    Outcome = "CONFIRMED"
	Refuted      Outcome = "REFUTED"
	Inconclusive Outcome = "INCONCLUSIVE"
)

// valid combos are those where the promiscuous state matches what was requested; a "no
// promisc" combo observed while another process had the uplink promiscuous proves nothing.
func (c ComboResult) valid() bool { return c.PromiscFlag == c.Promisc }

// EvalUnicast evaluates hypothesis 1: advertising the host MAC makes unicast NUD
// solicitations visible without promiscuous mode. Control: a combo that ran with promiscuous
// mode must have seen unicast NS, otherwise the stimulus did not work and nothing is concluded.
func EvalUnicast(rs []ComboResult) (Outcome, string) {
	var np, p, npRan int
	for _, c := range rs {
		if !c.UcastRan || !c.valid() {
			continue
		}
		if c.Promisc {
			p += c.Ucast.UcastNS
		} else {
			npRan++
			np += c.Ucast.UcastNS
		}
	}
	switch {
	case np > 0:
		return Confirmed, fmt.Sprintf("unicast NS seen without promiscuous mode (%d); with promisc %d", np, p)
	case npRan > 0 && p > 0:
		return Refuted, fmt.Sprintf("no unicast NS without promiscuous mode, but %d with it", p)
	default:
		return Inconclusive, "no unicast NS observed in any valid combination (stimulus did not work, or the promiscuous state was not as requested)"
	}
}

// EvalMulticast evaluates hypothesis 2: joining the solicited-node group makes multicast NS
// visible without promiscuous mode.
func EvalMulticast(rs []ComboResult) (Outcome, string) {
	var joined, notJoined, promisc, ranJoined int
	for _, c := range rs {
		if !c.McastRan || !c.valid() {
			continue
		}
		switch {
		case c.Promisc:
			promisc += c.Mcast.McastNS
		case c.Joined:
			ranJoined++
			joined += c.Mcast.McastNS
		default:
			notJoined += c.Mcast.McastNS
		}
	}
	need := "baseline without join saw them too: joining may not be needed on this NIC/switch"
	if notJoined == 0 {
		need = "baseline without join saw none: joining is what makes them visible"
	}
	switch {
	case joined > 0:
		return Confirmed, fmt.Sprintf("joined+no promisc saw %d multicast NS; not joined+no promisc saw %d (%s)", joined, notJoined, need)
	case ranJoined > 0 && promisc > 0:
		return Refuted, fmt.Sprintf("joined+no promisc saw 0 multicast NS but promisc saw %d", promisc)
	default:
		return Inconclusive, "no multicast NS observed in any valid combination (stimulus did not work, or the promiscuous state was not as requested)"
	}
}
