package ndp

import (
	"fmt"
	"net"
	"net/netip"

	"golang.org/x/net/ipv6"
)

// Joined holds the solicited-node group memberships; Close leaves them.
type Joined struct {
	pc     *ipv6.PacketConn
	conn   net.PacketConn
	Groups []netip.Addr
}

// JoinSolicitedNode joins the solicited-node multicast group of every target on iface through
// a raw ICMPv6 socket, so the NIC multicast filter passes the group without promiscuous mode.
// UNVERIFIED on FreeBSD 15: that a raw ip6:ipv6-icmp socket may JoinGroup, that the join
// reaches the NIC filter (MLD report, ifmcstat shows the group), and that closing the socket
// leaves the group.
func JoinSolicitedNode(iface string, targets []netip.Addr) (*Joined, error) {
	ifi, err := net.InterfaceByName(iface)
	if err != nil {
		return nil, err
	}
	c, err := net.ListenPacket("ip6:ipv6-icmp", "::")
	if err != nil {
		return nil, fmt.Errorf("raw icmpv6 socket: %w", err)
	}
	pc := ipv6.NewPacketConn(c)
	// Receive nothing: the socket exists only to hold the membership.
	var f ipv6.ICMPFilter
	f.SetAll(true)
	_ = pc.SetICMPFilter(&f)
	j := &Joined{pc: pc, conn: c}
	seen := map[netip.Addr]bool{}
	for _, t := range targets {
		g := SolicitedNode(t)
		if seen[g] {
			continue
		}
		seen[g] = true
		if err := pc.JoinGroup(ifi, &net.UDPAddr{IP: g.AsSlice()}); err != nil {
			c.Close()
			return nil, fmt.Errorf("join %s on %s: %w", g, iface, err)
		}
		j.Groups = append(j.Groups, g)
	}
	return j, nil
}

// Close leaves all groups.
func (j *Joined) Close() error {
	if j == nil {
		return nil
	}
	return j.conn.Close()
}
