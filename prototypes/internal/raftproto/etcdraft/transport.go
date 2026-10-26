package etcdraft

import (
	"encoding/binary"
	"io"
	"net"
	"sync"
	"time"

	pb "go.etcd.io/raft/v3/raftpb"
	"google.golang.org/protobuf/proto"
)

const maxFrame = 64 << 20

// transport is a minimal framed-TCP transport: 4-byte length then a protobuf Message.
// One goroutine and one connection per peer; a full queue drops the message and reports the
// peer unreachable, which raft treats like a lost packet.
type transport struct {
	self   uint64
	ln     net.Listener
	dial   func(string, time.Duration) (net.Conn, error)
	mu     sync.Mutex
	peers  map[uint64]*peer
	step   func(*pb.Message)
	report func(id uint64, snap bool, ok bool, msgSnap bool)
	closed chan struct{}
	wg     sync.WaitGroup
}

type peer struct {
	id   uint64
	addr string
	ch   chan *pb.Message
	stop chan struct{}
}

func newTransport(self uint64, ln net.Listener, dial func(string, time.Duration) (net.Conn, error)) *transport {
	t := &transport{self: self, ln: ln, dial: dial, peers: map[uint64]*peer{}, closed: make(chan struct{})}
	t.wg.Add(1)
	go t.accept()
	return t
}

func (t *transport) addPeer(id uint64, addr string) {
	if id == t.self {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if p, ok := t.peers[id]; ok {
		if p.addr == addr {
			return
		}
		close(p.stop)
	}
	p := &peer{id: id, addr: addr, ch: make(chan *pb.Message, 1024), stop: make(chan struct{})}
	t.peers[id] = p
	t.wg.Add(1)
	go t.sender(p)
}

func (t *transport) removePeer(id uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if p, ok := t.peers[id]; ok {
		close(p.stop)
		delete(t.peers, id)
	}
}

func (t *transport) send(msgs []*pb.Message) {
	for _, m := range msgs {
		t.mu.Lock()
		p := t.peers[m.GetTo()]
		t.mu.Unlock()
		if p == nil {
			t.report(m.GetTo(), false, false, m.GetType() == pb.MsgSnap)
			continue
		}
		select {
		case p.ch <- m:
		default:
			t.report(m.GetTo(), false, false, m.GetType() == pb.MsgSnap)
		}
	}
}

func (t *transport) sender(p *peer) {
	defer t.wg.Done()
	var c net.Conn
	defer func() {
		if c != nil {
			c.Close()
		}
	}()
	for {
		select {
		case <-t.closed:
			return
		case <-p.stop:
			return
		case m := <-p.ch:
			ok := false
			for attempt := 0; attempt < 2 && !ok; attempt++ {
				if c == nil {
					var err error
					c, err = t.dial(p.addr, 500*time.Millisecond)
					if err != nil {
						c = nil
						break
					}
				}
				if err := writeFrame(c, m); err != nil {
					c.Close()
					c = nil
					continue
				}
				ok = true
			}
			t.report(p.id, true, ok, m.GetType() == pb.MsgSnap)
		}
	}
}

func writeFrame(c net.Conn, m *pb.Message) error {
	b, err := proto.Marshal(m)
	if err != nil {
		return err
	}
	hdr := make([]byte, 4, 4+len(b))
	binary.BigEndian.PutUint32(hdr, uint32(len(b)))
	c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err = c.Write(append(hdr, b...))
	return err
}

func (t *transport) accept() {
	defer t.wg.Done()
	for {
		c, err := t.ln.Accept()
		if err != nil {
			return
		}
		t.wg.Add(1)
		go func() {
			defer t.wg.Done()
			defer c.Close()
			go func() { <-t.closed; c.Close() }()
			for {
				var hdr [4]byte
				if _, err := io.ReadFull(c, hdr[:]); err != nil {
					return
				}
				n := binary.BigEndian.Uint32(hdr[:])
				if n > maxFrame {
					return
				}
				b := make([]byte, n)
				if _, err := io.ReadFull(c, b); err != nil {
					return
				}
				m := &pb.Message{}
				if proto.Unmarshal(b, m) != nil {
					return
				}
				t.step(m)
			}
		}()
	}
}

func (t *transport) close() {
	select {
	case <-t.closed:
		return
	default:
	}
	close(t.closed)
	t.ln.Close()
	t.wg.Wait()
}
