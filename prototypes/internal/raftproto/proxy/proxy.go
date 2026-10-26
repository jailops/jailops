// Package proxy is a partition-capable TCP proxy. Every node advertises the proxy's listener
// for itself; peers dial it with a one-line preamble naming the dialing node, so the proxy can
// drop traffic between groups without iptables or pf. Partitions are symmetric.
package proxy

import (
	"bufio"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

const preamble = "JOPFROM "

type conn struct {
	from, to string
	a, b     net.Conn
}

// Net is the proxy fabric.
type Net struct {
	mu     sync.Mutex
	real   map[string]string
	ls     map[string]net.Listener
	group  map[string]int // nil means fully connected
	conns  map[*conn]struct{}
	closed bool
}

// New returns an empty fabric.
func New() *Net {
	return &Net{real: map[string]string{}, ls: map[string]net.Listener{}, conns: map[*conn]struct{}{}}
}

// Register starts a proxy listener for node id that forwards to realAddr. It returns the
// address peers must use.
func (n *Net) Register(id, realAddr string) (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	n.mu.Lock()
	n.real[id] = realAddr
	n.ls[id] = l
	n.mu.Unlock()
	go n.accept(id, l)
	return l.Addr().String(), nil
}

// Allowed reports whether from may talk to to under the current partition.
func (n *Net) Allowed(from, to string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.allowedLocked(from, to)
}

func (n *Net) allowedLocked(from, to string) bool {
	if n.group == nil {
		return true
	}
	gf, ok1 := n.group[from]
	gt, ok2 := n.group[to]
	return ok1 && ok2 && gf == gt
}

// Partition splits the fabric into groups; nodes in no group can reach nobody.
// Established connections that now cross a boundary are cut.
func (n *Net) Partition(groups ...[]string) {
	n.mu.Lock()
	n.group = map[string]int{}
	for i, g := range groups {
		for _, id := range g {
			n.group[id] = i
		}
	}
	var cut []*conn
	for c := range n.conns {
		if !n.allowedLocked(c.from, c.to) {
			cut = append(cut, c)
		}
	}
	n.mu.Unlock()
	for _, c := range cut {
		c.a.Close()
		c.b.Close()
	}
}

// Heal removes the partition.
func (n *Net) Heal() {
	n.mu.Lock()
	n.group = nil
	n.mu.Unlock()
}

// Close stops every listener and connection.
func (n *Net) Close() {
	n.mu.Lock()
	n.closed = true
	for _, l := range n.ls {
		l.Close()
	}
	var all []*conn
	for c := range n.conns {
		all = append(all, c)
	}
	n.mu.Unlock()
	for _, c := range all {
		c.a.Close()
		c.b.Close()
	}
}

func (n *Net) accept(to string, l net.Listener) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		go n.handle(to, c)
	}
}

func (n *Net) handle(to string, c net.Conn) {
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	br := bufio.NewReaderSize(c, 64)
	line, err := br.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, preamble) {
		c.Close()
		return
	}
	c.SetReadDeadline(time.Time{})
	from := strings.TrimSpace(strings.TrimPrefix(line, preamble))
	n.mu.Lock()
	ok := n.allowedLocked(from, to)
	realAddr := n.real[to]
	closed := n.closed
	n.mu.Unlock()
	if !ok || closed {
		c.Close()
		return
	}
	up, err := net.DialTimeout("tcp", realAddr, time.Second)
	if err != nil {
		c.Close()
		return
	}
	pc := &conn{from: from, to: to, a: c, b: up}
	n.mu.Lock()
	// the partition may have changed while dialing
	if !n.allowedLocked(from, to) {
		n.mu.Unlock()
		c.Close()
		up.Close()
		return
	}
	n.conns[pc] = struct{}{}
	n.mu.Unlock()
	done := make(chan struct{}, 2)
	go func() { io.Copy(up, br); done <- struct{}{} }()
	go func() { io.Copy(c, up); done <- struct{}{} }()
	<-done
	c.Close()
	up.Close()
	n.mu.Lock()
	delete(n.conns, pc)
	n.mu.Unlock()
}

// Dial returns a dial function for node from, to be used by transports. addr must be an
// address returned by Register.
func Dial(from string) func(addr string, timeout time.Duration) (net.Conn, error) {
	return func(addr string, timeout time.Duration) (net.Conn, error) {
		c, err := net.DialTimeout("tcp", addr, timeout)
		if err != nil {
			return nil, err
		}
		if _, err := fmt.Fprintf(c, "%s%s\n", preamble, from); err != nil {
			c.Close()
			return nil, err
		}
		return c, nil
	}
}

// FreeAddr returns a currently free 127.0.0.1 address from a fixed low range (20000-29999),
// away from the ephemeral range, so a port that was probed and closed is not stolen by an
// outgoing connection before the node binds it.
func FreeAddr() (string, error) {
	var last error
	for i := 0; i < 200; i++ {
		var b [2]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", err
		}
		port := 20000 + int(binary.BigEndian.Uint16(b[:]))%10000
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		l, err := net.Listen("tcp", addr)
		if err == nil {
			l.Close()
			return addr, nil
		}
		last = err
	}
	return "", last
}
