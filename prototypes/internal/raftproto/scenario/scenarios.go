package scenario

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"jailops.net/jailops/prototypes/internal/raftproto/api"
	"jailops.net/jailops/prototypes/internal/raftproto/kv"
)

// Params tunes the scenario run.
type Params struct {
	Ops      int // tiny commands in the throughput scenario
	Parallel int // concurrent writers in the parallel throughput run
}

// Result is one scenario outcome for one library.
type Result struct {
	Lib     string
	Name    string
	OK      bool
	Detail  string
	Dur     time.Duration
	Metrics map[string]float64
}

// Scenario is one named test.
type Scenario struct {
	Name string
	Run  func(c Cluster, p Params, m map[string]float64) (string, error)
}

// Names in the order they are run and printed.
var All = []Scenario{
	{"election-replication", electionReplication},
	{"crash-restart", crashRestart},
	{"leader-change", leaderChange},
	{"partition", partition},
	{"snapshot-compaction", snapshotCompaction},
	{"quorum-loss-recovery", quorumLoss},
	{"member-add-remove", memberChange},
	{"throughput", throughput},
}

// Config for scenarios needing aggressive compaction.
func scenarioConfig(base Config, name string) Config {
	if name == "snapshot-compaction" {
		base.SnapEvery, base.Trailing = 50, 10
	}
	return base
}

// Run executes the scenarios for one library; each gets a fresh three-node cluster in its own
// directory under base.Dir.
func Run(base Config, p Params, only func(name string) bool, progress func(string)) []Result {
	var out []Result
	for _, sc := range All {
		if only != nil && !only(sc.Name) {
			continue
		}
		cfg := scenarioConfig(base, sc.Name)
		cfg.Dir = fmt.Sprintf("%s/%s-%s", base.Dir, base.Lib, sc.Name)
		if progress != nil {
			progress(fmt.Sprintf("%s/%s", base.Lib, sc.Name))
		}
		r := Result{Lib: base.Lib, Name: sc.Name, Metrics: map[string]float64{}}
		t0 := time.Now()
		func() {
			c := NewCluster(cfg)
			defer c.Close()
			defer func() {
				if x := recover(); x != nil {
					r.OK, r.Detail = false, fmt.Sprintf("panic: %v", x)
				}
			}()
			if err := c.Start(3); err != nil {
				r.Detail = "start: " + err.Error()
				return
			}
			d, err := sc.Run(c, p, r.Metrics)
			r.OK, r.Detail = err == nil, d
			if err != nil {
				r.Detail = d + " ERROR: " + err.Error()
			}
		}()
		r.Dur = time.Since(t0)
		out = append(out, r)
	}
	return out
}

const (
	wait   = 15 * time.Second
	shortW = 8 * time.Second
)

func writeN(c Cluster, prefix string, n int) error {
	for i := 0; i < n; i++ {
		if err := c.Propose(kv.Command{Op: "set", K: fmt.Sprintf("%s-%d", prefix, i), X: fmt.Sprint(i)}, wait); err != nil {
			return fmt.Errorf("write %s-%d: %w", prefix, i, err)
		}
	}
	return nil
}

func hasKeys(c Cluster, i int, prefix string, n int) error {
	for k := 0; k < n; k++ {
		key := fmt.Sprintf("%s-%d", prefix, k)
		v, ok, err := c.Get(i, key)
		if err != nil || !ok || v != fmt.Sprint(k) {
			return fmt.Errorf("slot %d missing %s (ok=%v err=%v)", i, key, ok, err)
		}
	}
	return nil
}

// convergeAt waits until every listed node has applied at least min commands and all agree. A
// restarted node may legitimately report an empty FSM until the leader tells it the commit
// index, so plain equality is not enough.
func convergeAt(c Cluster, min uint64, idxs ...int) (api.Status, error) {
	deadline := time.Now().Add(wait)
	for {
		st, err := c.WaitConverged(time.Second, idxs...)
		if err == nil && st.Count >= min {
			return st, nil
		}
		if time.Now().After(deadline) {
			if err == nil {
				err = fmt.Errorf("converged at %d cmds, expected at least %d", st.Count, min)
			}
			return st, err
		}
	}
}

func followers(c Cluster, leader int) []int {
	var out []int
	for _, i := range c.Live() {
		if i != leader {
			out = append(out, i)
		}
	}
	return out
}

func electionReplication(c Cluster, p Params, m map[string]float64) (string, error) {
	t0 := time.Now()
	l, err := c.Leader(wait)
	if err != nil {
		return "", err
	}
	m["election_ms"] = float64(time.Since(t0).Milliseconds())
	if err := writeN(c, "a", 100); err != nil {
		return "", err
	}
	st, err := c.WaitConverged(wait)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("leader=n%d elected in %dms, 100 writes, 3 replicas equal at %d cmds hash %.8s", l+1, int(m["election_ms"]), st.Count, st.Hash), nil
}

func crashRestart(c Cluster, p Params, m map[string]float64) (string, error) {
	l, err := c.Leader(wait)
	if err != nil {
		return "", err
	}
	if err := writeN(c, "a", 50); err != nil {
		return "", err
	}
	f := followers(c, l)[0]
	if err := c.Crash(f); err != nil {
		return "", err
	}
	if err := writeN(c, "b", 50); err != nil {
		return "", fmt.Errorf("with one follower down: %w", err)
	}
	if err := c.Restart(f); err != nil {
		return "", err
	}
	pre, err := c.WaitConverged(wait)
	if err != nil {
		return "", fmt.Errorf("follower did not catch up after restart: %w", err)
	}
	// crash everything, restart everything: data must come back from disk
	for _, i := range c.Live() {
		if err := c.Crash(i); err != nil {
			return "", err
		}
	}
	t0 := time.Now()
	for i := 0; i < c.Slots(); i++ {
		if err := c.Restart(i); err != nil {
			return "", err
		}
	}
	if _, err := c.Leader(wait); err != nil {
		return "", fmt.Errorf("no leader after full restart: %w", err)
	}
	m["full_restart_ms"] = float64(time.Since(t0).Milliseconds())
	post, err := convergeAt(c, pre.Count)
	if err != nil {
		return "", err
	}
	if post.Count != pre.Count || post.Hash != pre.Hash {
		return "", fmt.Errorf("state changed over full crash: %d/%.8s -> %d/%.8s", pre.Count, pre.Hash, post.Count, post.Hash)
	}
	if err := writeN(c, "c", 10); err != nil {
		return "", err
	}
	if _, err := c.WaitConverged(wait); err != nil {
		return "", err
	}
	return fmt.Sprintf("follower crash+catch-up ok; all 3 crashed and restarted in %dms with identical FSM (%d cmds, hash %.8s); writes continue", int(m["full_restart_ms"]), post.Count, post.Hash), nil
}

func leaderChange(c Cluster, p Params, m map[string]float64) (string, error) {
	l, err := c.Leader(wait)
	if err != nil {
		return "", err
	}
	if err := writeN(c, "a", 20); err != nil {
		return "", err
	}
	if err := c.Crash(l); err != nil {
		return "", err
	}
	t0 := time.Now()
	nl, err := c.Leader(wait)
	if err != nil {
		return "", err
	}
	m["failover_ms"] = float64(time.Since(t0).Milliseconds())
	if err := writeN(c, "b", 20); err != nil {
		return "", err
	}
	if err := c.Restart(l); err != nil {
		return "", err
	}
	st, err := c.WaitConverged(wait)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("leader n%d killed, n%d took over in %dms, 20 more writes, old leader rejoined and converged (%d cmds)", l+1, nl+1, int(m["failover_ms"]), st.Count), nil
}

func partition(c Cluster, p Params, m map[string]float64) (string, error) {
	l, err := c.Leader(wait)
	if err != nil {
		return "", err
	}
	if err := writeN(c, "a", 20); err != nil {
		return "", err
	}
	if _, err := c.WaitConverged(wait); err != nil {
		return "", err
	}
	major := followers(c, l)
	c.Partition([]int{l}, major)
	// the isolated old leader must not be able to commit
	minErr := c.ProposeVia(l, kv.Command{Op: "set", K: "minority", X: "x"}, 2*time.Second)
	if minErr == nil {
		return "", errors.New("SAFETY: isolated minority node committed a write")
	}
	nl, err := c.LeaderAmong(major, wait)
	if err != nil {
		return "", fmt.Errorf("majority elected no leader: %w", err)
	}
	if err := c.ProposeAmong(major, kv.Command{Op: "set", K: "majority", X: "y"}, wait); err != nil {
		return "", fmt.Errorf("majority could not commit: %w", err)
	}
	stepped := true
	if st, err := c.Status(l); err == nil && st.Leader {
		stepped = false
		// UNVERIFIED-neutral observation: some libraries keep a stale leader flag until they hear the new term
	}
	c.Heal()
	st, err := c.WaitConverged(wait)
	if err != nil {
		return "", err
	}
	for _, i := range c.Live() {
		if _, ok, _ := c.Get(i, "minority"); ok {
			return "", fmt.Errorf("SAFETY: uncommitted minority write survived on slot %d", i)
		}
		if v, ok, _ := c.Get(i, "majority"); !ok || v != "y" {
			return "", fmt.Errorf("majority write missing on slot %d", i)
		}
	}
	return fmt.Sprintf("isolated leader n%d: write failed (%v); majority elected n%d and committed; after heal all converged (%d cmds), minority write discarded; stale leader flag cleared before heal=%v", l+1, trim(minErr), nl+1, st.Count, stepped), nil
}

func trim(err error) string {
	s := err.Error()
	if len(s) > 60 {
		s = s[:60] + "..."
	}
	return s
}

func snapshotCompaction(c Cluster, p Params, m map[string]float64) (string, error) {
	l, err := c.Leader(wait)
	if err != nil {
		return "", err
	}
	if err := writeN(c, "a", 200); err != nil {
		return "", err
	}
	if _, err := c.WaitConverged(wait); err != nil {
		return "", err
	}
	for _, i := range c.Live() {
		if err := c.Snapshot(i); err != nil {
			return "", fmt.Errorf("snapshot n%d: %w", i+1, err)
		}
		st, _ := c.Status(i)
		if st.FirstIdx <= 1 {
			return "", fmt.Errorf("n%d log not compacted after snapshot: first=%d last=%d", i+1, st.FirstIdx, st.LastIdx)
		}
		m[fmt.Sprintf("first_idx_n%d", i+1)] = float64(st.FirstIdx)
	}
	f := followers(c, l)[0]
	before, _ := c.Status(f)
	if err := c.Crash(f); err != nil {
		return "", err
	}
	if err := writeN(c, "b", 200); err != nil {
		return "", err
	}
	for _, i := range c.Live() {
		if err := c.Snapshot(i); err != nil {
			return "", err
		}
	}
	cur, err := c.Leader(wait)
	if err != nil {
		return "", err
	}
	ls, _ := c.Status(cur)
	if ls.FirstIdx <= before.LastIdx {
		return "", fmt.Errorf("leader log (first=%d) still covers the crashed follower (last=%d): snapshot path not forced", ls.FirstIdx, before.LastIdx)
	}
	if err := c.Restart(f); err != nil {
		return "", err
	}
	st, err := c.WaitConverged(wait)
	if err != nil {
		return "", fmt.Errorf("follower did not catch up through snapshot: %w", err)
	}
	after, _ := c.Status(f)
	if after.SnapIndex <= before.LastIdx {
		return "", fmt.Errorf("follower snap index %d not past its old log end %d: catch-up was not by snapshot", after.SnapIndex, before.LastIdx)
	}
	for _, i := range c.Live() {
		if err := hasKeys(c, i, "b", 200); err != nil {
			return "", err
		}
	}
	for _, i := range slices.Clone(c.Live()) {
		c.Crash(i)
	}
	for i := 0; i < c.Slots(); i++ {
		if err := c.Restart(i); err != nil {
			return "", err
		}
	}
	st2, err := convergeAt(c, st.Count)
	if err != nil {
		return "", fmt.Errorf("after full restart from snapshots: %w", err)
	}
	if st2.Hash != st.Hash {
		return "", fmt.Errorf("hash changed across restart from snapshot")
	}
	return fmt.Sprintf("logs compacted on all nodes (first idx > 1); follower n%d (old last idx %d) caught up via snapshot (snap idx %d); full restart from snapshot+log kept hash %.8s (%d cmds)", f+1, before.LastIdx, after.SnapIndex, st2.Hash, st2.Count), nil
}

func quorumLoss(c Cluster, p Params, m map[string]float64) (string, error) {
	l, err := c.Leader(wait)
	if err != nil {
		return "", err
	}
	if err := writeN(c, "a", 30); err != nil {
		return "", err
	}
	pre, err := c.WaitConverged(wait)
	if err != nil {
		return "", err
	}
	fs := followers(c, l)
	survivor := fs[0]
	for _, i := range []int{l, fs[1]} {
		if err := c.Crash(i); err != nil {
			return "", err
		}
	}
	t0 := time.Now()
	if err := c.ProposeVia(survivor, kv.Command{Op: "set", K: "lost", X: "x"}, 2*time.Second); err == nil {
		return "", errors.New("SAFETY: write succeeded without quorum")
	}
	m["write_refused_ms"] = float64(time.Since(t0).Milliseconds())
	if err := c.ForceRecover(survivor); err != nil {
		return "", fmt.Errorf("recover: %w", err)
	}
	if _, err := c.LeaderAmong([]int{survivor}, wait); err != nil {
		return "", fmt.Errorf("recovered node did not become leader: %w", err)
	}
	if err := hasKeys(c, survivor, "a", 30); err != nil {
		return "", fmt.Errorf("committed data lost by recovery: %w", err)
	}
	st, _ := c.Status(survivor)
	if st.Count < pre.Count {
		return "", fmt.Errorf("recovered count %d < committed %d", st.Count, pre.Count)
	}
	if err := c.ProposeAmong([]int{survivor}, kv.Command{Op: "set", K: "after", X: "1"}, wait); err != nil {
		return "", fmt.Errorf("write after recovery: %w", err)
	}
	// rebuild a 3-member cluster with two fresh nodes
	for k := 0; k < 2; k++ {
		if _, err := c.AddMember(); err != nil {
			return "", err
		}
	}
	live := []int{survivor, c.Slots() - 2, c.Slots() - 1}
	if err := writeN(c, "d", 10); err != nil {
		return "", err
	}
	end, err := c.WaitConverged(wait, live...)
	if err != nil {
		return "", err
	}
	if len(end.Members) != 3 {
		return "", fmt.Errorf("members after rebuild: %v", end.Members)
	}
	return fmt.Sprintf("2 of 3 killed: write refused; recovered survivor n%d (all %d committed keys present, %d cmds), accepted writes, two fresh members joined, 3-member cluster converged (%d cmds, members %v)", survivor+1, 30, st.Count, end.Count, end.Members), nil
}

func memberChange(c Cluster, p Params, m map[string]float64) (string, error) {
	l, err := c.Leader(wait)
	if err != nil {
		return "", err
	}
	if err := writeN(c, "a", 20); err != nil {
		return "", err
	}
	for k := 0; k < 2; k++ {
		if _, err := c.AddMember(); err != nil {
			return "", err
		}
	}
	st, err := c.WaitConverged(wait)
	if err != nil {
		return "", fmt.Errorf("after adding two members: %w", err)
	}
	if len(st.Members) != 5 {
		return "", fmt.Errorf("expected 5 members, have %v", st.Members)
	}
	f := followers(c, l)[0]
	if err := c.RemoveMember(f); err != nil {
		return "", err
	}
	if err := writeN(c, "b", 10); err != nil {
		return "", err
	}
	l2, err := c.Leader(wait)
	if err != nil {
		return "", err
	}
	t0 := time.Now()
	if err := c.RemoveMember(l2); err != nil {
		return "", err
	}
	nl, err := c.Leader(wait)
	if err != nil {
		return "", fmt.Errorf("no leader after removing the leader: %w", err)
	}
	m["leader_removal_ms"] = float64(time.Since(t0).Milliseconds())
	if err := writeN(c, "c", 10); err != nil {
		return "", err
	}
	end, err := c.WaitConverged(wait)
	if err != nil {
		return "", err
	}
	if len(end.Members) != 3 || len(c.Live()) != 3 {
		return "", fmt.Errorf("expected 3 members, have %v", end.Members)
	}
	return fmt.Sprintf("3 -> 5 members, removed follower n%d and leader n%d; n%d leads after %dms; final 3 members %v converged (%d cmds)", f+1, l2+1, nl+1, int(m["leader_removal_ms"]), end.Members, end.Count), nil
}

func throughput(c Cluster, p Params, m map[string]float64) (string, error) {
	ops := p.Ops
	if ops <= 0 {
		ops = 300
	}
	par := p.Parallel
	if par <= 0 {
		par = 16
	}
	if _, err := c.Leader(wait); err != nil {
		return "", err
	}
	// warm up
	if err := writeN(c, "w", 20); err != nil {
		return "", err
	}
	lat := make([]time.Duration, 0, ops)
	t0 := time.Now()
	for i := 0; i < ops; i++ {
		s := time.Now()
		if err := c.Propose(kv.Command{Op: "set", K: fmt.Sprintf("s%d", i%64), X: "v"}, wait); err != nil {
			return "", err
		}
		lat = append(lat, time.Since(s))
	}
	seq := time.Since(t0)
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	m["seq_ops_s"] = float64(ops) / seq.Seconds()
	m["seq_p50_ms"] = float64(lat[len(lat)/2].Microseconds()) / 1000
	m["seq_p99_ms"] = float64(lat[len(lat)*99/100].Microseconds()) / 1000
	var wg sync.WaitGroup
	var mu sync.Mutex
	var first error
	t1 := time.Now()
	per := ops / par
	for w := 0; w < par; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < per; i++ {
				if err := c.Propose(kv.Command{Op: "add", K: fmt.Sprintf("p%d", w), X: "1"}, wait); err != nil {
					mu.Lock()
					if first == nil {
						first = err
					}
					mu.Unlock()
					return
				}
			}
		}()
	}
	wg.Wait()
	if first != nil {
		return "", first
	}
	m["par_ops_s"] = float64(per*par) / time.Since(t1).Seconds()
	if _, err := c.WaitConverged(wait); err != nil {
		return "", err
	}
	return fmt.Sprintf("fsync on, 3 nodes: sequential %.0f ops/s (p50 %.2fms p99 %.2fms, %d ops); %d writers %.0f ops/s", m["seq_ops_s"], m["seq_p50_ms"], m["seq_p99_ms"], ops, par, m["par_ops_s"]), nil
}
