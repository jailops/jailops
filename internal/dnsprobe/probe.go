// Package dnsprobe asks a DNS server one question in a raw UDP packet to see whether it answers. It is not a resolver:
// the answer is never used, only whether there was one and what its code was. The standard resolver is no help here:
// it answers `localhost` from /etc/hosts and never asks the server.
package dnsprobe

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"net"
	"strings"
	"time"
)

// The DNS types this package asks for.
const (
	TypeA  uint16 = 1
	TypeNS uint16 = 2
)

// Status is what the server did with the question.
type Status int

const (
	NoReply  Status = iota // nothing came back (nothing listens, a filter, a timeout)
	Refused                // the server answered REFUSED: it listens but its access list says no
	ServFail               // the server answered SERVFAIL: it listens but cannot resolve (an upstream that is gone)
	Answered               // any other answer
)

// Result is the outcome of one probe.
type Result struct {
	Status Status
	Err    string // why there was no reply, for a message
}

// Up says whether the server took the question and answered it in a way that serves a client: a SERVFAIL is a server
// that is there, a REFUSED is one that will not serve this client.
func (r Result) Up() bool { return r.Status == Answered || r.Status == ServFail }

// Func asks addr (an address, with ":port" when it is not 53) for the records of a name, and waits at most timeout.
type Func func(ctx context.Context, addr, name string, qtype uint16, timeout time.Duration) Result

// Query is the Func that sends the packet. It makes one attempt.
func Query(ctx context.Context, addr, name string, qtype uint16, timeout time.Duration) Result {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		host, port = strings.Trim(addr, "[]"), "53"
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "udp", net.JoinHostPort(host, port))
	if err != nil {
		return Result{Err: err.Error()}
	}
	defer conn.Close()
	dl, _ := ctx.Deadline()
	_ = conn.SetDeadline(dl)
	var id [2]byte
	_, _ = rand.Read(id[:])
	q := packet(binary.BigEndian.Uint16(id[:]), name, qtype)
	if _, err := conn.Write(q); err != nil {
		return Result{Err: err.Error()}
	}
	buf := make([]byte, 1500)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return Result{Err: err.Error()}
		}
		if n < 12 || buf[0] != q[0] || buf[1] != q[1] || buf[2]&0x80 == 0 { // not the answer to this question
			continue
		}
		switch buf[3] & 0x0f {
		case 5:
			return Result{Status: Refused}
		case 2:
			return Result{Status: ServFail}
		}
		return Result{Status: Answered}
	}
}

// Wait probes up to tries times, pausing a little between the attempts (a server that restarts takes a moment), and
// returns the first answer that is up, else the last result.
func Wait(ctx context.Context, f Func, addr, name string, qtype uint16, tries int, each time.Duration) Result {
	var r Result
	for i := 0; i < tries; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return r
			case <-time.After(200 * time.Millisecond):
			}
		}
		if r = f(ctx, addr, name, qtype, each); r.Up() {
			return r
		}
	}
	return r
}

// packet is a question with recursion desired: the header, one name, the type and the class IN.
func packet(id uint16, name string, qtype uint16) []byte {
	b := make([]byte, 12, 64)
	binary.BigEndian.PutUint16(b[0:], id)
	binary.BigEndian.PutUint16(b[2:], 0x0100)
	binary.BigEndian.PutUint16(b[4:], 1)
	for _, label := range strings.Split(strings.TrimSuffix(name, "."), ".") {
		if label == "" {
			continue
		}
		b = append(b, byte(len(label)))
		b = append(b, label...)
	}
	b = append(b, 0)
	b = binary.BigEndian.AppendUint16(b, qtype)
	return binary.BigEndian.AppendUint16(b, 1)
}
