package replicate

import (
	"context"
	"sync"
	"testing"
	"time"

	"jailops.net/jailops/sdk/api"
)

var (
	ctx   = context.Background()
	epoch = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
)

const (
	hostA = "host-a"
	root  = "tank/jailops/replicas"
	src   = "tank/jailops/enc/jails/web/data"
)

type env struct {
	t     *testing.T
	host  *memZFS
	nodeZ *memZFS
	cat   *MemCatalog
	node  *Target
	now   time.Time
	mu    sync.Mutex
	audit []AuditEvent
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, host: newMem(), nodeZ: newMem(), cat: NewMemCatalog(), now: epoch.Add(30 * 24 * time.Hour)}
	n, err := NewTarget(TargetConfig{Root: root, ZFS: e.nodeZ, Catalog: e.cat, Now: func() time.Time { return e.now },
		Audit: func(a AuditEvent) { e.mu.Lock(); e.audit = append(e.audit, a); e.mu.Unlock() }, DeletionDelay: 48 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	e.node = n
	return e
}

// snap takes a source snapshot, encrypted dataset by default.
func (e *env) snap(name string, day int) uint64 {
	return e.host.Snap(src, name, epoch.Add(time.Duration(day)*24*time.Hour), true)
}

func (e *env) job() Job {
	return Job{Sender: LocalSource{e.host}, Receiver: e.node.ForHost(hostA), Keeper: LocalSource{e.host}, Source: src, ChainID: "web",
		HostUUID: hostA, JailUUID: "jail-1", Jail: "web", Raw: true, Encrypted: true, Keys: escrowed(), Policy: "nightly"}
}

func escrowed() KeyStatus {
	return KeyStatus{State: KeyEscrowed, KeyID: "k1", History: KeyHistory{{KeyID: "k1", From: epoch}},
		Escrow: &Escrow{KeyID: "k1", Covers: []string{"k1"}, Recipients: []string{"age1recovery"}, BlobRef: "blob/host-a.age"}}
}

func (e *env) push(snap string) Result {
	e.t.Helper()
	j := e.job()
	j.Snapshot = snap
	r, err := Push(ctx, j)
	if err != nil {
		e.t.Fatalf("push %s: %v", snap, err)
	}
	return r
}

func (e *env) chainDS() string { return root + "/" + hostA + "/web" }

func (e *env) audited(action, outcome string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, a := range e.audit {
		if a.Action == action && a.Outcome == outcome {
			n++
		}
	}
	return n
}

func wantCode(t *testing.T, err error, c api.Code) {
	t.Helper()
	if err == nil || !api.IsCode(err, c) {
		t.Fatalf("want code %s, got %v", c, err)
	}
}

func holdsOf(t *testing.T, z *memZFS, ds, snap string) []string {
	t.Helper()
	h, err := z.Holds(ctx, ds, snap)
	if err != nil {
		t.Fatal(err)
	}
	return h
}
