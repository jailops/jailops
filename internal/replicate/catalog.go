package replicate

import (
	"context"
	"sync"
	"time"

	"jailops.net/jailops/sdk/api"
)

// Replica is the record of one snapshot received on a chain.
type Replica struct {
	// Name is ReplicaName(chain, snapshot).
	Name      string
	CreatedAt time.Time
	Spec      ReplicaSpec
	Status    ReplicaStatus
}

// ReplicaSpec identifies what was replicated.
type ReplicaSpec struct {
	Jail     string
	JailUUID string
	// HostUUID is the source host.
	HostUUID string
	Policy   string
	// Chain is the chain id on the receiving host.
	Chain    string
	Snapshot string
	// FromGUID is zero for a full stream.
	FromGUID, ToGUID uint64
	// Raw is true when the stream was a raw send: the dataset stays
	// encrypted at rest on the receiving host.
	Raw bool
	// Bundle is the restore closure of the jail at the time of the snapshot.
	Bundle api.RestoreBundle
}

// ReplicaStatus is the state recorded on the receiving host.
type ReplicaStatus struct {
	Bytes      int64
	Verified   bool
	VerifiedAt time.Time
	// Held is true while the snapshot carries its keep hold.
	Held bool
	// PendingDeletion is set when an admin requested deletion; the delay
	// before it takes effect protects against mistakes and compromise.
	PendingDeletion *time.Time
}

// Catalog persists chains and Replica records on the receiving host. The
// production implementation lives with the state store; MemCatalog serves
// tests and single-process use.
type Catalog interface {
	Chain(ctx context.Context, hostUUID, chainID string) (Chain, bool, error)
	PutChain(ctx context.Context, c Chain) error
	PutReplica(ctx context.Context, r Replica) error
	Replica(ctx context.Context, name string) (Replica, bool, error)
	Replicas(ctx context.Context, hostUUID, chainID string) ([]Replica, error)
	DeleteReplica(ctx context.Context, name string) error
}

// ReplicaName is the object name of a Replica.
func ReplicaName(chainID, snapshot string) string { return chainID + "@" + snapshot }

// MemCatalog is an in-memory Catalog.
type MemCatalog struct {
	mu       sync.Mutex
	chains   map[string]Chain
	replicas map[string]Replica
}

// NewMemCatalog returns an empty catalog.
func NewMemCatalog() *MemCatalog {
	return &MemCatalog{chains: map[string]Chain{}, replicas: map[string]Replica{}}
}

func ck(h, c string) string { return h + "/" + c }

func (m *MemCatalog) Chain(_ context.Context, h, c string) (Chain, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ch, ok := m.chains[ck(h, c)]
	return ch.Clone(), ok, nil
}

func (m *MemCatalog) PutChain(_ context.Context, c Chain) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.chains[ck(c.HostUUID, c.ID)] = c.Clone()
	return nil
}

func (m *MemCatalog) PutReplica(_ context.Context, r Replica) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.replicas[r.Name] = r
	return nil
}

func (m *MemCatalog) Replica(_ context.Context, name string) (Replica, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.replicas[name]
	return r, ok, nil
}

func (m *MemCatalog) Replicas(_ context.Context, h, c string) ([]Replica, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Replica
	for _, r := range m.replicas {
		if r.Spec.HostUUID == h && r.Spec.Chain == c {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *MemCatalog) DeleteReplica(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.replicas, name)
	return nil
}
