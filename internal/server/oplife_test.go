package server_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"jailops.net/jailops/internal/daemon"
	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/internal/sys/zfs"
	"jailops.net/jailops/sdk/api"
)

// lifeZFS holds or fails the creation of a snapshot, so that a test can keep an operation in flight and end it the
// way it needs: released (success), failed or cancelled.
type lifeZFS struct {
	zfs.Client
	mu   sync.Mutex
	gate chan struct{}
	hit  chan struct{}
	fail error
}

func (z *lifeZFS) hold() (hit <-chan struct{}, release func()) {
	z.mu.Lock()
	defer z.mu.Unlock()
	z.gate, z.hit = make(chan struct{}), make(chan struct{}, 4)
	gate := z.gate
	var once sync.Once
	return z.hit, func() { once.Do(func() { close(gate) }) }
}

func (z *lifeZFS) failWith(err error) {
	z.mu.Lock()
	z.fail = err
	z.mu.Unlock()
}

func (z *lifeZFS) Snapshot(ctx context.Context, o zfs.SnapshotOpts) error {
	z.mu.Lock()
	gate, hit, fail := z.gate, z.hit, z.fail
	z.mu.Unlock()
	if gate != nil {
		hit <- struct{}{}
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if fail != nil {
		return fail
	}
	return z.Client.Snapshot(ctx, o)
}

// The life of an asynchronous operation as a client sees it: accepted with 202 and a Location, running (with
// may_cancel), a second request on the locked jail refused as busy, cancelled, ended as a failure with its error, and
// ended as a success; a finished operation cannot be cancelled and a wait on it answers at once. The transcript is
// frozen in testdata/wire/operation_lifecycle.golden.
func TestFreezeWireOperationLifecycle(t *testing.T) {
	var z *lifeZFS
	e := dtest.New(t, dtest.Options{Clients: func(c *daemon.Clients) { z = &lifeZFS{Client: c.ZFS}; c.ZFS = z }})
	e.Seed()
	w := newWire(t, e)
	r := w.do("POST", "/api/v1/jails", webBody)
	w.wait(r.opID())
	settled(t, e, "web")

	waitHit := func(hit <-chan struct{}) {
		t.Helper()
		select {
		case <-hit:
		case <-time.After(10 * time.Second):
			t.Fatal("the operation never started")
		}
	}

	w.note("accepted: 202, Location, the record of a request that has not finished; it holds the jail's lock")
	hit, release := z.hold()
	defer release()
	snap := w.do("POST", "/api/v1/jails/web/snapshots", `{"name":"slow"}`)
	waitHit(hit)

	w.note("running: the record says so and says it may be cancelled")
	w.do("GET", "/api/v1/operations/"+snap.opID(), "")
	w.note("a wait that ends before the operation does answers with the running record")
	w.do("GET", "/api/v1/operations/"+snap.opID()+"/wait?timeout=0", "")

	w.note("busy: another change of the locked jail is refused with 409 and the code busy, a read is not")
	w.do("PUT", "/api/v1/jails/web/state", `{"action":"start"}`)
	w.do("GET", "/api/v1/jails/web", "")

	w.note("cancel: 200 with an empty body, the operation ends cancelled and may no longer be cancelled")
	w.do("DELETE", "/api/v1/operations/"+snap.opID(), "")
	w.wait(snap.opID())
	w.note("cancelling a finished operation is refused with 400 invalid")
	w.do("DELETE", "/api/v1/operations/"+snap.opID(), "")
	w.note("the lock is gone: the same request succeeds now, and nothing of the cancelled one is left")
	release()
	z.mu.Lock()
	z.gate, z.hit = nil, nil
	z.mu.Unlock()
	w.do("GET", "/api/v1/jails/web/snapshots", "")

	w.note("failure: the operation ends as failure; its error has the code and the message a synchronous request would have answered")
	z.failWith(api.Errorf(api.CodePoolExhausted, "the pool has no space left"))
	bad := w.do("POST", "/api/v1/jails/web/snapshots", `{"name":"full"}`)
	w.wait(bad.opID())
	w.note("a failed operation is not cancellable either")
	w.do("DELETE", "/api/v1/operations/"+bad.opID(), "")
	z.failWith(nil)

	w.note("success: the final record, the result in its metadata; a repeat of the same request is refused, not repeated")
	ok := w.do("POST", "/api/v1/jails/web/snapshots", `{"name":"good"}`)
	w.wait(ok.opID())
	w.do("POST", "/api/v1/jails/web/snapshots", `{"name":"good"}`)

	w.note("a wait on a finished operation answers at once with the final record; an unknown operation is not found")
	w.do("GET", "/api/v1/operations/"+ok.opID()+"/wait?timeout=30", "")
	w.do("GET", "/api/v1/operations/00000000-0000-4000-8000-000000000000/wait?timeout=1", "")
	w.do("DELETE", "/api/v1/operations/00000000-0000-4000-8000-000000000000", "")

	w.check("operation_lifecycle")
}
