// Package etcdraft adapts go.etcd.io/raft/v3 to the api.Node contract.
//
// The library is a state machine only: the adapter supplies the durable log (store.go), the
// transport (transport.go), the ticker, the apply loop, snapshots, conf-change handling and
// forced single-member recovery. That is the main cost of this library and it shows in the
// comparison table as adapter size.
//
// UNVERIFIED: behaviour on FreeBSD (bbolt, TCP) is assumed identical to Linux.
package etcdraft

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"

	raft "go.etcd.io/raft/v3"
	pb "go.etcd.io/raft/v3/raftpb"
	"google.golang.org/protobuf/proto"

	"jailops.net/jailops/prototypes/internal/raftproto/api"
	"jailops.net/jailops/prototypes/internal/raftproto/kv"
	"jailops.net/jailops/prototypes/internal/raftproto/proxy"
)

func numID(s string) (uint64, error) {
	if len(s) < 2 || s[0] != 'n' {
		return 0, fmt.Errorf("etcdraft: node id %q must look like n<number>", s)
	}
	return strconv.ParseUint(s[1:], 10, 64)
}

func strID(i uint64) string { return "n" + strconv.FormatUint(i, 10) }

type snapData struct {
	KV    []byte            `json:"kv"`
	Peers map[uint64]string `json:"peers"`
}

// Node implements api.Node.
type Node struct {
	spec api.NodeSpec

	mu      sync.Mutex // guards the fields below
	running bool
	removed bool
	node    raft.Node
	mem     *raft.MemoryStorage
	st      *store
	tr      *transport
	fsm     *kv.FSM
	conf    *pb.ConfState
	peers   map[uint64]string
	applied uint64
	snapIdx uint64
	waiters map[string]chan error
	stop    chan struct{}
	done    chan struct{}
	snapReq chan chan error
}

// New returns a stopped node.
func New(spec api.NodeSpec) *Node { return &Node{spec: spec} }

func (n *Node) ID() string { return n.spec.ID }

func (n *Node) Start(o api.StartOpts) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.running {
		return errors.New("already started")
	}
	self, err := numID(n.spec.ID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(n.spec.Dir, 0o700); err != nil {
		return err
	}
	st, err := openStore(filepath.Join(n.spec.Dir, "raft.bolt"), n.spec.NoSync)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			st.Close()
		}
	}()
	ld, err := st.load()
	if err != nil {
		return err
	}
	has := ld.hs != nil || ld.snap != nil || len(ld.ents) > 0
	peers := map[uint64]string{}
	if ld.peers != nil {
		if err := json.Unmarshal(ld.peers, &peers); err != nil {
			return err
		}
	}
	if o.ForceRecover {
		if !has {
			return errors.New("force recover: no existing state")
		}
		if err := forceNewCluster(st, &ld, self); err != nil {
			return err
		}
	}
	mem := raft.NewMemoryStorage()
	fsm := kv.New()
	var snapIdx uint64
	var conf *pb.ConfState
	if ld.snap != nil && !raft.IsEmptySnap(ld.snap) {
		if err := mem.ApplySnapshot(ld.snap); err != nil {
			return err
		}
		var sd snapData
		if err := json.Unmarshal(ld.snap.GetData(), &sd); err != nil {
			return err
		}
		if err := fsm.Restore(sd.KV); err != nil {
			return err
		}
		snapIdx = ld.snap.GetMetadata().GetIndex()
		conf = proto.Clone(ld.snap.GetMetadata().GetConfState()).(*pb.ConfState)
		for k, v := range sd.Peers {
			peers[k] = v
		}
	}
	if ld.hs != nil {
		if err := mem.SetHardState(ld.hs); err != nil {
			return err
		}
	}
	var tail []*pb.Entry
	for _, e := range ld.ents {
		if e.GetIndex() > snapIdx {
			tail = append(tail, e)
		}
	}
	if err := mem.Append(tail); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", n.spec.Bind)
	if err != nil {
		return err
	}
	cfg := &raft.Config{
		ID: self, ElectionTick: 10, HeartbeatTick: 1, Storage: mem, Applied: snapIdx,
		MaxSizePerMsg: 1 << 20, MaxInflightMsgs: 256, CheckQuorum: true, PreVote: true,
		Logger: &raft.DefaultLogger{Logger: log.New(io.Discard, "", 0)},
	}
	n.mem, n.st, n.fsm, n.conf, n.peers = mem, st, fsm, conf, peers
	n.applied, n.snapIdx = snapIdx, snapIdx
	n.waiters = map[string]chan error{}
	n.stop, n.done, n.snapReq = make(chan struct{}), make(chan struct{}), make(chan chan error)
	fsm.OnApply = func(id string, err error) {
		n.mu.Lock()
		w := n.waiters[id]
		delete(n.waiters, id)
		n.mu.Unlock()
		if w != nil {
			w <- err
		}
	}
	tr := newTransport(self, ln, proxy.Dial(n.spec.ID))
	n.tr = tr
	switch {
	case has:
		n.node = raft.RestartNode(cfg)
	case len(o.Bootstrap) > 0:
		var rp []raft.Peer
		for _, p := range o.Bootstrap {
			id, err := numID(p.ID)
			if err != nil {
				ln.Close()
				return err
			}
			rp = append(rp, raft.Peer{ID: id, Context: []byte(p.Addr)})
			peers[id] = p.Addr
		}
		b, _ := json.Marshal(peers)
		if err := st.savePeers(b); err != nil {
			ln.Close()
			return err
		}
		n.node = raft.StartNode(cfg, rp)
	default: // join: empty state, wait for the leader's messages
		for _, p := range o.Join {
			if id, err := numID(p.ID); err == nil {
				peers[id] = p.Addr
			}
		}
		n.node = raft.RestartNode(cfg)
	}
	for id, a := range peers {
		tr.addPeer(id, a)
	}
	node := n.node
	tr.step = func(m *pb.Message) { node.Step(context.Background(), m) }
	tr.report = func(id uint64, _ bool, ok bool, msgSnap bool) {
		if !ok {
			node.ReportUnreachable(id)
			if msgSnap {
				node.ReportSnapshot(id, raft.SnapshotFailure)
			}
		} else if msgSnap {
			node.ReportSnapshot(id, raft.SnapshotFinish)
		}
	}
	n.running = true
	ok = true
	go n.loop(node, mem, st, tr, fsm, n.stop, n.done)
	return nil
}

// forceNewCluster follows the recipe of a forced new cluster: compute the voter set from the
// snapshot and the log, append committed ConfChange entries that remove every other voter, and
// commit everything in the log. UNVERIFIED hazard documented: entries that were appended but not
// yet committed become committed, exactly as in that recipe.
func forceNewCluster(st *store, ld *loaded, self uint64) error {
	voters := map[uint64]bool{}
	if ld.snap != nil && ld.snap.GetMetadata() != nil {
		for _, v := range ld.snap.GetMetadata().GetConfState().GetVoters() {
			voters[v] = true
		}
	}
	var last uint64
	if ld.snap != nil {
		last = ld.snap.GetMetadata().GetIndex()
	}
	for _, e := range ld.ents {
		if e.GetIndex() > last {
			last = e.GetIndex()
		}
		if e.GetType() == pb.EntryConfChange {
			var cc pb.ConfChange
			if err := proto.Unmarshal(e.GetData(), &cc); err != nil {
				return err
			}
			switch cc.GetType() {
			case pb.ConfChangeAddNode:
				voters[cc.GetNodeId()] = true
			case pb.ConfChangeRemoveNode:
				delete(voters, cc.GetNodeId())
			}
		}
	}
	hs := ld.hs
	if hs == nil {
		hs = &pb.HardState{}
	}
	var add []*pb.Entry
	ids := make([]uint64, 0, len(voters))
	for v := range voters {
		ids = append(ids, v)
	}
	slices.Sort(ids)
	for _, v := range ids {
		if v == self {
			continue
		}
		last++
		cc := &pb.ConfChange{Type: pb.ConfChangeRemoveNode.Enum(), NodeId: proto.Uint64(v)}
		data, err := proto.Marshal(cc)
		if err != nil {
			return err
		}
		add = append(add, &pb.Entry{Term: proto.Uint64(hs.GetTerm()), Index: proto.Uint64(last), Type: pb.EntryConfChange.Enum(), Data: data})
	}
	hs = &pb.HardState{Term: hs.Term, Vote: hs.Vote, Commit: proto.Uint64(last)}
	if len(add) > 0 || hs.GetCommit() != ld.hs.GetCommit() {
		if err := st.save(hs, add, nil); err != nil {
			return err
		}
	}
	ld.hs = hs
	ld.ents = append(ld.ents, add...)
	return nil
}

func (n *Node) loop(node raft.Node, mem *raft.MemoryStorage, st *store, tr *transport, fsm *kv.FSM, stop, done chan struct{}) {
	defer close(done)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
			node.Tick()
		case reply := <-n.snapReq:
			reply <- n.makeSnapshot(mem, st, fsm, true)
		case rd := <-node.Ready():
			if err := st.save(rd.HardState, rd.Entries, rd.Snapshot); err != nil {
				panic(fmt.Sprintf("etcdraft: persist failed: %v", err))
			}
			if !raft.IsEmptySnap(rd.Snapshot) {
				mem.ApplySnapshot(rd.Snapshot)
				var sd snapData
				if json.Unmarshal(rd.Snapshot.GetData(), &sd) == nil && fsm.Restore(sd.KV) == nil {
					n.mu.Lock()
					n.applied = rd.Snapshot.GetMetadata().GetIndex()
					n.snapIdx = n.applied
					n.conf = proto.Clone(rd.Snapshot.GetMetadata().GetConfState()).(*pb.ConfState)
					n.mu.Unlock()
					for id, a := range sd.Peers {
						n.setPeer(tr, st, id, a, false)
					}
				}
			}
			if !raft.IsEmptyHardState(rd.HardState) {
				mem.SetHardState(rd.HardState)
			}
			mem.Append(rd.Entries)
			tr.send(rd.Messages)
			for _, e := range rd.CommittedEntries {
				n.apply(node, tr, st, fsm, e)
			}
			n.mu.Lock()
			due := n.applied-n.snapIdx >= uint64(max(n.spec.SnapEvery, 8))
			n.mu.Unlock()
			if due {
				n.makeSnapshot(mem, st, fsm, false)
			}
			node.Advance()
		}
	}
}

func (n *Node) setPeer(tr *transport, st *store, id uint64, addr string, remove bool) {
	n.mu.Lock()
	if remove {
		delete(n.peers, id)
	} else {
		n.peers[id] = addr
	}
	b, _ := json.Marshal(n.peers)
	n.mu.Unlock()
	if remove {
		tr.removePeer(id)
	} else {
		tr.addPeer(id, addr)
	}
	st.savePeers(b)
}

func (n *Node) apply(node raft.Node, tr *transport, st *store, fsm *kv.FSM, e *pb.Entry) {
	n.mu.Lock()
	if e.GetIndex() <= n.applied {
		n.mu.Unlock()
		return
	}
	n.mu.Unlock()
	switch e.GetType() {
	case pb.EntryNormal:
		if len(e.GetData()) > 0 {
			fsm.Apply(e.GetData())
		}
	case pb.EntryConfChange:
		var cc pb.ConfChange
		if proto.Unmarshal(e.GetData(), &cc) != nil {
			break
		}
		cs := node.ApplyConfChange(&cc)
		n.mu.Lock()
		n.conf = proto.Clone(cs).(*pb.ConfState)
		n.mu.Unlock()
		switch cc.GetType() {
		case pb.ConfChangeAddNode:
			n.setPeer(tr, st, cc.GetNodeId(), string(cc.GetContext()), false)
		case pb.ConfChangeRemoveNode:
			n.setPeer(tr, st, cc.GetNodeId(), "", true)
			if self, _ := numID(n.spec.ID); cc.GetNodeId() == self {
				n.mu.Lock()
				n.removed = true
				n.mu.Unlock()
			}
		}
	}
	n.mu.Lock()
	n.applied = e.GetIndex()
	n.mu.Unlock()
}

// makeSnapshot runs on the loop goroutine, so the FSM and applied index are consistent.
func (n *Node) makeSnapshot(mem *raft.MemoryStorage, st *store, fsm *kv.FSM, force bool) error {
	n.mu.Lock()
	applied, conf := n.applied, proto.Clone(n.conf).(*pb.ConfState)
	peers := map[uint64]string{}
	for k, v := range n.peers {
		peers[k] = v
	}
	if applied <= n.snapIdx {
		n.mu.Unlock()
		if force {
			return nil
		}
		return nil
	}
	n.mu.Unlock()
	kb, err := fsm.Snapshot()
	if err != nil {
		return err
	}
	data, _ := json.Marshal(snapData{KV: kb, Peers: peers})
	snap, err := mem.CreateSnapshot(applied, conf, data)
	if err != nil {
		return err
	}
	trail := uint64(max(n.spec.Trailing, 4))
	compact := uint64(0)
	if applied > trail {
		compact = applied - trail
	}
	if first, _ := mem.FirstIndex(); compact < first {
		compact = first - 1
	}
	if err := st.saveSnapshot(snap, compact); err != nil {
		return err
	}
	if compact > 0 {
		if err := mem.Compact(compact); err != nil && !errors.Is(err, raft.ErrCompacted) {
			return err
		}
	}
	n.mu.Lock()
	n.snapIdx = applied
	n.mu.Unlock()
	return nil
}

func (n *Node) shutdown() error {
	n.mu.Lock()
	if !n.running {
		n.mu.Unlock()
		return nil
	}
	n.running = false
	stop, done, node, tr, st := n.stop, n.done, n.node, n.tr, n.st
	n.mu.Unlock()
	close(stop)
	<-done
	node.Stop()
	tr.close()
	return st.Close()
}

// Crash and Stop are the same in process: the loop is stopped, nothing is flushed beyond what
// each Ready already fsynced. Process mode kills with SIGKILL instead.
func (n *Node) Crash() error { return n.shutdown() }
func (n *Node) Stop() error  { return n.shutdown() }

func (n *Node) live() (raft.Node, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.running {
		return nil, errors.New("node not running")
	}
	return n.node, nil
}

func (n *Node) Status() (api.Status, error) {
	node, err := n.live()
	if err != nil {
		return api.Status{}, err
	}
	bs := node.Status()
	n.mu.Lock()
	defer n.mu.Unlock()
	s := api.Status{
		Leader: bs.RaftState == raft.StateLeader, Term: bs.GetTerm(), Removed: n.removed, SnapIndex: n.snapIdx,
	}
	if bs.Lead != 0 {
		s.LeaderID = strID(bs.Lead)
	}
	s.Count, s.Hash = n.fsm.State()
	s.FirstIdx, _ = n.mem.FirstIndex()
	s.LastIdx, _ = n.mem.LastIndex()
	for _, v := range n.conf.GetVoters() {
		s.Members = append(s.Members, strID(v))
	}
	slices.Sort(s.Members)
	return s, nil
}

func (n *Node) Propose(cmd []byte, timeout time.Duration) error {
	node, err := n.live()
	if err != nil {
		return err
	}
	var c kv.Command
	if err := json.Unmarshal(cmd, &c); err != nil || c.ID == "" {
		return errors.New("etcdraft: command needs an id")
	}
	w := make(chan error, 1)
	n.mu.Lock()
	n.waiters[c.ID] = w
	n.mu.Unlock()
	drop := func() {
		n.mu.Lock()
		delete(n.waiters, c.ID)
		n.mu.Unlock()
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := node.Propose(ctx, cmd); err != nil {
		drop()
		return err
	}
	select {
	case err := <-w:
		return err
	case <-ctx.Done():
		drop()
		return fmt.Errorf("etcdraft: not committed within %v", timeout)
	}
}

func (n *Node) Get(k string) (string, bool, error) {
	if _, err := n.live(); err != nil {
		return "", false, err
	}
	v, ok := n.fsm.Get(k)
	return v, ok, nil
}

func (n *Node) Snapshot() error {
	if _, err := n.live(); err != nil {
		return err
	}
	reply := make(chan error, 1)
	select {
	case n.snapReq <- reply:
		return <-reply
	case <-time.After(5 * time.Second):
		return errors.New("snapshot request timed out")
	}
}

func (n *Node) confChange(cc *pb.ConfChange, timeout time.Duration, done func(members []string) bool) error {
	node, err := n.live()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := node.ProposeConfChange(ctx, cc); err != nil {
		return err
	}
	for ctx.Err() == nil {
		if s, err := n.Status(); err == nil && done(s.Members) {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return errors.New("etcdraft: membership change not visible in time (dropped while another change was pending, or no leader)")
}

func (n *Node) AddVoter(id, addr string, timeout time.Duration) error {
	nid, err := numID(id)
	if err != nil {
		return err
	}
	cc := &pb.ConfChange{Type: pb.ConfChangeAddNode.Enum(), NodeId: proto.Uint64(nid), Context: []byte(addr)}
	return n.confChange(cc, timeout, func(m []string) bool { return slices.Contains(m, id) })
}

func (n *Node) RemoveVoter(id string, timeout time.Duration) error {
	nid, err := numID(id)
	if err != nil {
		return err
	}
	cc := &pb.ConfChange{Type: pb.ConfChangeRemoveNode.Enum(), NodeId: proto.Uint64(nid)}
	return n.confChange(cc, timeout, func(m []string) bool { return !slices.Contains(m, id) })
}

var _ api.Node = (*Node)(nil)
