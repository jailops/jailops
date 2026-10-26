//go:build freebsd

package ndproxy

import (
	"errors"
	"net"
	"net/netip"
	"sync"
	"syscall"

	"golang.org/x/net/ipv6"
)

// maxGroupsPerSocket stays well below the per-socket membership limit.
// UNVERIFIED: the limit differs between FreeBSD versions.
const maxGroupsPerSocket = 15

type joinKey struct {
	iface string
	group netip.Addr
}

type joinSock struct {
	conn net.PacketConn
	pc   *ipv6.PacketConn
	n    int
}

// MulticastJoiner joins solicited-node groups with ordinary IPv6 multicast
// sockets, which makes the kernel send MLD reports and accept the group's
// Ethernet multicast address without promiscuous mode.
//
// UNVERIFIED: that this really makes the NS visible to BPF without
// promiscuous mode is hypothesis H2 of prototype 5.
type MulticastJoiner struct {
	mu    sync.Mutex
	socks []*joinSock
	where map[joinKey]*joinSock
}

// NewMulticastJoiner returns a Joiner.
func NewMulticastJoiner() *MulticastJoiner {
	return &MulticastJoiner{where: map[joinKey]*joinSock{}}
}

func (j *MulticastJoiner) join(s *joinSock, iface string, group netip.Addr) error {
	ifi, err := net.InterfaceByName(iface)
	if err != nil {
		return err
	}
	err = s.pc.JoinGroup(ifi, &net.UDPAddr{IP: group.AsSlice()})
	if errors.Is(err, syscall.EADDRINUSE) {
		return nil // already a member
	}
	return err
}

// Join implements Joiner and is idempotent.
func (j *MulticastJoiner) Join(iface string, group netip.Addr) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	k := joinKey{iface, group}
	if s, ok := j.where[k]; ok {
		return j.join(s, iface, group) // re-join after the link returned
	}
	var s *joinSock
	for _, c := range j.socks {
		if c.n < maxGroupsPerSocket {
			s = c
			break
		}
	}
	if s == nil {
		conn, err := net.ListenPacket("udp6", "[::1]:0") // group membership is per interface, the bound address is irrelevant: keep the socket off the network
		if err != nil {
			return err
		}
		s = &joinSock{conn: conn, pc: ipv6.NewPacketConn(conn)}
		j.socks = append(j.socks, s)
	}
	if err := j.join(s, iface, group); err != nil {
		return err
	}
	s.n++
	j.where[k] = s
	return nil
}

// Leave implements Joiner.
func (j *MulticastJoiner) Leave(iface string, group netip.Addr) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	k := joinKey{iface, group}
	s, ok := j.where[k]
	if !ok {
		return nil
	}
	delete(j.where, k)
	s.n--
	ifi, err := net.InterfaceByName(iface)
	if err != nil {
		return nil // interface gone: the kernel dropped the membership
	}
	return s.pc.LeaveGroup(ifi, &net.UDPAddr{IP: group.AsSlice()})
}

// Close releases all memberships.
func (j *MulticastJoiner) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	var errs []error
	for _, s := range j.socks {
		errs = append(errs, s.conn.Close())
	}
	j.socks, j.where = nil, map[joinKey]*joinSock{}
	return errors.Join(errs...)
}
