// Package api is the library-neutral node contract the scenario driver is written against.
package api

import "time"

// Peer is a cluster member as seen by transports: id and the address peers dial (the proxy).
type Peer struct {
	ID   string `json:"id"`
	Addr string `json:"addr"`
}

// NodeSpec says where and how a node runs.
type NodeSpec struct {
	Lib       string `json:"lib"` // "hashicorp" or "etcd"
	ID        string `json:"id"`  // "n1", "n2", ...
	Dir       string `json:"dir"`
	Bind      string `json:"bind"` // real listen address
	Adv       string `json:"adv"`  // address peers use (proxy)
	SnapEvery int    `json:"snap_every"`
	Trailing  int    `json:"trailing"`
	NoSync    bool   `json:"no_sync"`
}

// StartOpts controls how Start initialises the node.
type StartOpts struct {
	// Bootstrap lists the initial cluster for a brand-new node. Empty means restart from disk,
	// or join an existing cluster (an empty data dir with no peers).
	Bootstrap []Peer `json:"bootstrap,omitempty"`
	// Join lists the addresses of existing members for a node that joins an empty data dir. The
	// etcd adapter needs it so the newcomer can answer the leader before it has seen the log;
	// hashicorp ignores it because the leader dials the newcomer.
	Join []Peer `json:"join,omitempty"`
	// ForceRecover rewrites the cluster configuration of existing on-disk state so that this
	// node is the only voter (quorum-loss recovery).
	ForceRecover bool `json:"force_recover,omitempty"`
}

// Status is a point-in-time view.
type Status struct {
	Leader    bool     `json:"leader"`
	LeaderID  string   `json:"leader_id"`
	Term      uint64   `json:"term"`
	Count     uint64   `json:"count"` // commands applied to the FSM
	Hash      string   `json:"hash"`
	FirstIdx  uint64   `json:"first_idx"` // first log index still on disk
	LastIdx   uint64   `json:"last_idx"`
	Members   []string `json:"members"`
	Removed   bool     `json:"removed,omitempty"`
	SnapIndex uint64   `json:"snap_index"`
}

// Node is one raft member of one library.
type Node interface {
	ID() string
	Start(opts StartOpts) error
	// Crash stops the node without any orderly shutdown the library can control. In-process it
	// closes everything abruptly; for a process-backed node it is SIGKILL.
	Crash() error
	// Stop is an orderly shutdown.
	Stop() error
	Status() (Status, error)
	// Propose replicates a command and returns once this node applied it (or the timeout).
	Propose(cmd []byte, timeout time.Duration) error
	Get(key string) (string, bool, error)
	// Snapshot forces a snapshot and log compaction on this node.
	Snapshot() error
	// AddVoter and RemoveVoter must be called on any node; they return when the membership
	// change is committed and visible in this node's Status.Members (or fail).
	AddVoter(id, addr string, timeout time.Duration) error
	RemoveVoter(id string, timeout time.Duration) error
}
