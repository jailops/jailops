// Package scenario drives the same test scenarios against any Raft library through the
// api.Node contract, in process or with one child process per node.
package scenario

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"jailops.net/jailops/prototypes/internal/raftproto/api"
	"jailops.net/jailops/prototypes/internal/raftproto/kv"
	"jailops.net/jailops/prototypes/internal/raftproto/proc"
	"jailops.net/jailops/prototypes/internal/raftproto/proxy"
)

// Cluster is what scenarios are written against. Indices are stable slot numbers; slot i has
// node id "n<i+1>". Removed and crashed nodes keep their slot.
type Cluster interface {
	Start(n int) error
	Close()
	Slots() int
	Live() []int
	Crash(i int) error
	Restart(i int) error
	Partition(groups ...[]int)
	Heal()
	Leader(timeout time.Duration) (int, error)
	LeaderAmong(idxs []int, timeout time.Duration) (int, error)
	Propose(c kv.Command, timeout time.Duration) error
	ProposeAmong(idxs []int, c kv.Command, timeout time.Duration) error
	ProposeVia(i int, c kv.Command, timeout time.Duration) error
	Snapshot(i int) error
	AddMember() (int, error)
	RemoveMember(i int) error
	// ForceRecover restarts slot i from its own disk state as the only voter of a new cluster.
	ForceRecover(i int) error
	Status(i int) (api.Status, error)
	Get(i int, key string) (string, bool, error)
	WaitConverged(timeout time.Duration, idxs ...int) (api.Status, error)
}

// Config selects library and mode.
type Config struct {
	Lib       string // hashicorp | etcd
	Processes bool
	Exe       string // binary for child processes
	Dir       string // parent directory for node data; created if empty
	SnapEvery int
	Trailing  int
	NoSync    bool
}

type slot struct {
	spec    api.NodeSpec
	node    api.Node
	up      bool
	removed bool
}

type cluster struct {
	cfg    Config
	net    *proxy.Net
	slots  []*slot
	hint   int
	prefix string
	seq    atomic.Uint64
	mu     sync.Mutex
}

// NewCluster returns an empty cluster; call Start.
func NewCluster(cfg Config) Cluster {
	b := make([]byte, 4)
	rand.Read(b)
	if cfg.SnapEvery == 0 {
		cfg.SnapEvery = 1000
	}
	if cfg.Trailing == 0 {
		cfg.Trailing = 50
	}
	return &cluster{cfg: cfg, net: proxy.New(), hint: -1, prefix: hex.EncodeToString(b)}
}

func (c *cluster) newSlot() (*slot, error) {
	i := len(c.slots)
	id := fmt.Sprintf("n%d", i+1)
	bind, err := proxy.FreeAddr()
	if err != nil {
		return nil, err
	}
	adv, err := c.net.Register(id, bind)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(c.cfg.Dir, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	spec := api.NodeSpec{Lib: c.cfg.Lib, ID: id, Dir: dir, Bind: bind, Adv: adv,
		SnapEvery: c.cfg.SnapEvery, Trailing: c.cfg.Trailing, NoSync: c.cfg.NoSync}
	var node api.Node
	if c.cfg.Processes {
		node = proc.NewRemote(spec, c.cfg.Exe)
	} else {
		node, err = proc.NewLocal(spec)
		if err != nil {
			return nil, err
		}
	}
	s := &slot{spec: spec, node: node}
	c.slots = append(c.slots, s)
	return s, nil
}

func (c *cluster) Start(n int) error {
	if c.cfg.Dir == "" {
		d, err := os.MkdirTemp("", "jop-proto-7-")
		if err != nil {
			return err
		}
		c.cfg.Dir = d
	}
	var peers []api.Peer
	for range n {
		s, err := c.newSlot()
		if err != nil {
			return err
		}
		peers = append(peers, api.Peer{ID: s.spec.ID, Addr: s.spec.Adv})
	}
	for _, s := range c.slots {
		if err := s.node.Start(api.StartOpts{Bootstrap: peers}); err != nil {
			return fmt.Errorf("start %s: %w", s.spec.ID, err)
		}
		s.up = true
	}
	return nil
}

func (c *cluster) Close() {
	for _, s := range c.slots {
		if s.up {
			s.node.Stop()
			s.up = false
		}
	}
	c.net.Close()
}

func (c *cluster) Slots() int { return len(c.slots) }

func (c *cluster) Live() []int {
	var out []int
	for i, s := range c.slots {
		if s.up && !s.removed {
			out = append(out, i)
		}
	}
	return out
}

func (c *cluster) Crash(i int) error {
	s := c.slots[i]
	err := s.node.Crash()
	s.up = false
	c.hint = -1
	return err
}

func (c *cluster) Restart(i int) error {
	s := c.slots[i]
	if s.up {
		return errors.New("already up")
	}
	if err := s.node.Start(api.StartOpts{}); err != nil {
		return err
	}
	s.up = true
	return nil
}

func (c *cluster) ids(idxs []int) []string {
	var out []string
	for _, i := range idxs {
		out = append(out, c.slots[i].spec.ID)
	}
	return out
}

func (c *cluster) Partition(groups ...[]int) {
	var g [][]string
	for _, x := range groups {
		g = append(g, c.ids(x))
	}
	c.net.Partition(g...)
	c.hint = -1
}

func (c *cluster) Heal() { c.net.Heal(); c.hint = -1 }

func (c *cluster) LeaderAmong(idxs []int, timeout time.Duration) (int, error) {
	deadline := time.Now().Add(timeout)
	for {
		best, bestTerm := -1, uint64(0)
		for _, i := range idxs {
			if !c.slots[i].up {
				continue
			}
			st, err := c.slots[i].node.Status()
			if err == nil && st.Leader && !st.Removed && (best < 0 || st.Term > bestTerm) {
				best, bestTerm = i, st.Term
			}
		}
		if best >= 0 {
			return best, nil
		}
		if time.Now().After(deadline) {
			return -1, fmt.Errorf("no leader among %v within %v", c.ids(idxs), timeout)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func (c *cluster) Leader(timeout time.Duration) (int, error) { return c.LeaderAmong(c.Live(), timeout) }

func (c *cluster) stamp(cmd kv.Command) []byte {
	if cmd.ID == "" {
		cmd.ID = fmt.Sprintf("%s-%d", c.prefix, c.seq.Add(1))
	}
	return kv.Encode(cmd)
}

func (c *cluster) ProposeVia(i int, cmd kv.Command, timeout time.Duration) error {
	return c.slots[i].node.Propose(c.stamp(cmd), timeout)
}

func (c *cluster) Propose(cmd kv.Command, timeout time.Duration) error {
	return c.ProposeAmong(c.Live(), cmd, timeout)
}

// ProposeAmong retries against the current leader among idxs until the deadline. The command
// keeps one id across retries; the key-value commands used here are idempotent, so a retry
// after an ambiguous timeout cannot change the final state.
func (c *cluster) ProposeAmong(idxs []int, cmd kv.Command, timeout time.Duration) error {
	if cmd.ID == "" {
		cmd.ID = fmt.Sprintf("%s-%d", c.prefix, c.seq.Add(1))
	}
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		c.mu.Lock()
		h := c.hint
		c.mu.Unlock()
		if h < 0 || !slices.Contains(idxs, h) || !c.slots[h].up {
			l, err := c.LeaderAmong(idxs, min(time.Until(deadline), time.Second))
			if err != nil {
				last = err
				continue
			}
			h = l
		}
		err := c.slots[h].node.Propose(kv.Encode(cmd), min(max(time.Until(deadline), 50*time.Millisecond), 3*time.Second))
		if err == nil {
			c.mu.Lock()
			c.hint = h
			c.mu.Unlock()
			return nil
		}
		last = err
		c.mu.Lock()
		c.hint = -1
		c.mu.Unlock()
		time.Sleep(30 * time.Millisecond)
	}
	return fmt.Errorf("propose not committed within %v: %v", timeout, last)
}

func (c *cluster) Snapshot(i int) error { return c.slots[i].node.Snapshot() }

func (c *cluster) AddMember() (int, error) {
	s, err := c.newSlot()
	if err != nil {
		return -1, err
	}
	i := len(c.slots) - 1
	var known []api.Peer
	for _, j := range c.liveExcept(i) {
		known = append(known, api.Peer{ID: c.slots[j].spec.ID, Addr: c.slots[j].spec.Adv})
	}
	if err := s.node.Start(api.StartOpts{Join: known}); err != nil {
		return i, err
	}
	s.up = true
	deadline := time.Now().Add(15 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		l, err := c.LeaderAmong(c.liveExcept(i), time.Second)
		if err != nil {
			last = err
			continue
		}
		if last = c.slots[l].node.AddVoter(s.spec.ID, s.spec.Adv, 5*time.Second); last == nil {
			return i, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return i, fmt.Errorf("add member: %v", last)
}

func (c *cluster) liveExcept(x int) []int {
	var out []int
	for _, i := range c.Live() {
		if i != x {
			out = append(out, i)
		}
	}
	return out
}

func (c *cluster) RemoveMember(i int) error {
	deadline := time.Now().Add(15 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		l, err := c.Leader(time.Second)
		if err != nil {
			last = err
			continue
		}
		if last = c.slots[l].node.RemoveVoter(c.slots[i].spec.ID, 5*time.Second); last == nil {
			c.slots[i].removed = true
			c.hint = -1
			c.Crash(i) // a removed node must not keep running as a ghost
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("remove member: %v", last)
}

func (c *cluster) ForceRecover(i int) error {
	s := c.slots[i]
	if s.up {
		if err := s.node.Stop(); err != nil {
			return err
		}
		s.up = false
	}
	if err := s.node.Start(api.StartOpts{ForceRecover: true}); err != nil {
		return err
	}
	s.up = true
	c.hint = -1
	return nil
}

func (c *cluster) Status(i int) (api.Status, error) { return c.slots[i].node.Status() }

func (c *cluster) Get(i int, k string) (string, bool, error) { return c.slots[i].node.Get(k) }

func (c *cluster) WaitConverged(timeout time.Duration, idxs ...int) (api.Status, error) {
	if len(idxs) == 0 {
		idxs = c.Live()
	}
	deadline := time.Now().Add(timeout)
	var msg string
	for {
		var first api.Status
		same := true
		msg = ""
		for k, i := range idxs {
			st, err := c.slots[i].node.Status()
			if err != nil {
				same, msg = false, fmt.Sprintf("%s: %v", c.slots[i].spec.ID, err)
				break
			}
			if k == 0 {
				first = st
			} else if st.Count != first.Count || st.Hash != first.Hash {
				same = false
			}
			msg += fmt.Sprintf(" %s=%d/%.8s", c.slots[i].spec.ID, st.Count, st.Hash)
		}
		if same {
			return first, nil
		}
		if time.Now().After(deadline) {
			return first, fmt.Errorf("not converged after %v:%s", timeout, msg)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
