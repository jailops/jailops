// Package hashi adapts hashicorp/raft with raft-boltdb to the api.Node contract.
//
// Everything goes through a custom StreamLayer so peers dial the partition proxy.
// UNVERIFIED: behaviour on FreeBSD (bbolt mmap and fsync, TCP) is assumed identical to Linux.
package hashi

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/raft"
	raftboltdb "github.com/hashicorp/raft-boltdb/v2"

	"jailops.net/jailops/prototypes/internal/raftproto/api"
	"jailops.net/jailops/prototypes/internal/raftproto/kv"
	"jailops.net/jailops/prototypes/internal/raftproto/proxy"
)

type stream struct {
	ln   net.Listener
	adv  net.Addr
	dial func(string, time.Duration) (net.Conn, error)
}

type advAddr string

func (a advAddr) Network() string { return "tcp" }
func (a advAddr) String() string  { return string(a) }

func (s *stream) Accept() (net.Conn, error) { return s.ln.Accept() }
func (s *stream) Close() error              { return s.ln.Close() }
func (s *stream) Addr() net.Addr            { return s.adv }
func (s *stream) Dial(a raft.ServerAddress, t time.Duration) (net.Conn, error) {
	return s.dial(string(a), t)
}

type fsmAdapter struct{ *kv.FSM }

func (f fsmAdapter) Apply(l *raft.Log) interface{} { return f.FSM.Apply(l.Data) }
func (f fsmAdapter) Snapshot() (raft.FSMSnapshot, error) {
	b, err := f.FSM.Snapshot()
	return snapshot(b), err
}
func (f fsmAdapter) Restore(rc io.ReadCloser) error {
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		return err
	}
	return f.FSM.Restore(b)
}

type snapshot []byte

func (s snapshot) Persist(sink raft.SnapshotSink) error {
	if _, err := sink.Write(s); err != nil {
		sink.Cancel()
		return err
	}
	return sink.Close()
}
func (s snapshot) Release() {}

// Node implements api.Node.
type Node struct {
	spec api.NodeSpec
	mu   sync.Mutex
	r    *raft.Raft
	fsm  *kv.FSM
	logs *raftboltdb.BoltStore
	trn  *raft.NetworkTransport
	snap *raft.FileSnapshotStore
	ln   net.Listener
}

// New returns a stopped node.
func New(spec api.NodeSpec) *Node { return &Node{spec: spec} }

func (n *Node) ID() string { return n.spec.ID }

func (n *Node) config() *raft.Config {
	c := raft.DefaultConfig()
	c.LocalID = raft.ServerID(n.spec.ID)
	c.HeartbeatTimeout = 200 * time.Millisecond
	c.ElectionTimeout = 200 * time.Millisecond
	c.LeaderLeaseTimeout = 100 * time.Millisecond
	c.CommitTimeout = 5 * time.Millisecond
	c.SnapshotInterval = 250 * time.Millisecond
	c.SnapshotThreshold = uint64(max(n.spec.SnapEvery, 8))
	c.TrailingLogs = uint64(max(n.spec.Trailing, 4))
	c.Logger = hclog.New(&hclog.LoggerOptions{Output: io.Discard, Level: hclog.Off})
	return c
}

// Start opens stores and the transport, bootstraps or recovers if asked, and starts raft.
func (n *Node) Start(o api.StartOpts) (err error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.r != nil {
		return errors.New("already started")
	}
	if err := os.MkdirAll(n.spec.Dir, 0o700); err != nil {
		return err
	}
	closers := []func(){}
	defer func() {
		if err != nil {
			for i := len(closers) - 1; i >= 0; i-- {
				closers[i]()
			}
		}
	}()
	ln, err := net.Listen("tcp", n.spec.Bind)
	if err != nil {
		return err
	}
	closers = append(closers, func() { ln.Close() })
	st := &stream{ln: ln, adv: advAddr(n.spec.Adv), dial: proxy.Dial(n.spec.ID)}
	trn := raft.NewNetworkTransportWithConfig(&raft.NetworkTransportConfig{
		Stream: st, MaxPool: 3, Timeout: 2 * time.Second,
		Logger: hclog.New(&hclog.LoggerOptions{Output: io.Discard, Level: hclog.Off}),
	})
	closers = append(closers, func() { trn.Close() })
	logs, err := raftboltdb.New(raftboltdb.Options{Path: filepath.Join(n.spec.Dir, "raft.db"), NoSync: n.spec.NoSync})
	if err != nil {
		return err
	}
	closers = append(closers, func() { logs.Close() })
	snaps, err := raft.NewFileSnapshotStore(n.spec.Dir, 2, io.Discard)
	if err != nil {
		return err
	}
	cfg := n.config()
	has, err := raft.HasExistingState(logs, logs, snaps)
	if err != nil {
		return err
	}
	switch {
	case o.ForceRecover:
		if !has {
			return errors.New("force recover: no existing state")
		}
		// RecoverCluster replays snapshot and log into a throwaway FSM, replaces the
		// configuration with this node alone and writes a new snapshot.
		conf := raft.Configuration{Servers: []raft.Server{{Suffrage: raft.Voter, ID: cfg.LocalID, Address: raft.ServerAddress(n.spec.Adv)}}}
		if err := raft.RecoverCluster(cfg, fsmAdapter{kv.New()}, logs, logs, snaps, trn, conf); err != nil {
			return fmt.Errorf("RecoverCluster: %w", err)
		}
	case !has && len(o.Bootstrap) > 0:
		var conf raft.Configuration
		for _, p := range o.Bootstrap {
			conf.Servers = append(conf.Servers, raft.Server{Suffrage: raft.Voter, ID: raft.ServerID(p.ID), Address: raft.ServerAddress(p.Addr)})
		}
		if err := raft.BootstrapCluster(cfg, logs, logs, snaps, trn, conf); err != nil {
			return fmt.Errorf("BootstrapCluster: %w", err)
		}
	}
	fsm := kv.New()
	r, err := raft.NewRaft(cfg, fsmAdapter{fsm}, logs, logs, snaps, trn)
	if err != nil {
		return err
	}
	n.r, n.fsm, n.logs, n.trn, n.snap, n.ln = r, fsm, logs, trn, snaps, ln
	return nil
}

func (n *Node) teardown(graceful bool) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.r == nil {
		return nil
	}
	f := n.r.Shutdown()
	err := f.Error()
	n.trn.Close()
	n.ln.Close()
	n.logs.Close()
	n.r = nil
	return err
}

// Crash: UNVERIFIED-free approximation. Shutdown() is the only way to release the bolt file in
// process; the on-disk state is whatever was fsynced. Real kills are done by the process mode.
func (n *Node) Crash() error { return n.teardown(false) }
func (n *Node) Stop() error  { return n.teardown(true) }

func (n *Node) get() (*raft.Raft, *kv.FSM, *raftboltdb.BoltStore, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.r == nil {
		return nil, nil, nil, errors.New("node not running")
	}
	return n.r, n.fsm, n.logs, nil
}

func (n *Node) Status() (api.Status, error) {
	r, fsm, logs, err := n.get()
	if err != nil {
		return api.Status{}, err
	}
	var s api.Status
	s.Leader = r.State() == raft.Leader
	_, id := r.LeaderWithID()
	s.LeaderID = string(id)
	s.Term = r.CurrentTerm()
	s.Count, s.Hash = fsm.State()
	s.FirstIdx, _ = logs.FirstIndex()
	s.LastIdx, _ = logs.LastIndex()
	f := r.GetConfiguration()
	if f.Error() == nil {
		for _, sv := range f.Configuration().Servers {
			s.Members = append(s.Members, string(sv.ID))
		}
		sort.Strings(s.Members)
	}
	if metas, err := n.snap.List(); err == nil && len(metas) > 0 {
		s.SnapIndex = metas[0].Index
	}
	return s, nil
}

func (n *Node) Propose(cmd []byte, timeout time.Duration) error {
	r, _, _, err := n.get()
	if err != nil {
		return err
	}
	f := r.Apply(cmd, timeout)
	if err := f.Error(); err != nil {
		return err
	}
	if e, ok := f.Response().(error); ok && e != nil {
		return e
	}
	return nil
}

func (n *Node) Get(k string) (string, bool, error) {
	_, fsm, _, err := n.get()
	if err != nil {
		return "", false, err
	}
	v, ok := fsm.Get(k)
	return v, ok, nil
}

func (n *Node) Snapshot() error {
	r, _, _, err := n.get()
	if err != nil {
		return err
	}
	return r.Snapshot().Error()
}

func (n *Node) AddVoter(id, addr string, timeout time.Duration) error {
	r, _, _, err := n.get()
	if err != nil {
		return err
	}
	return r.AddVoter(raft.ServerID(id), raft.ServerAddress(addr), 0, timeout).Error()
}

func (n *Node) RemoveVoter(id string, timeout time.Duration) error {
	r, _, _, err := n.get()
	if err != nil {
		return err
	}
	return r.RemoveServer(raft.ServerID(id), 0, timeout).Error()
}

var _ api.Node = (*Node)(nil)
