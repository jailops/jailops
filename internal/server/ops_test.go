package server_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"jailops.net/jailops/internal/daemon"
	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/internal/sys/zfs"
	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

// gateZFS blocks Snapshot until released or the context ends, so a test can
// hold an operation in flight.
type gateZFS struct {
	zfs.Client
	mu   sync.Mutex
	gate chan struct{}
	hit  chan struct{}
}

func (g *gateZFS) arm() (hit <-chan struct{}, release func()) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.gate, g.hit = make(chan struct{}), make(chan struct{}, 4)
	gate := g.gate
	return g.hit, func() { close(gate) }
}

func (g *gateZFS) Snapshot(ctx context.Context, o zfs.SnapshotOpts) error {
	g.mu.Lock()
	gate, hit := g.gate, g.hit
	g.mu.Unlock()
	if gate != nil {
		hit <- struct{}{}
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return g.Client.Snapshot(ctx, o)
}

func TestOperationCancelAndBusy(t *testing.T) {
	var gz *gateZFS
	e := dtest.New(t, dtest.Options{Clients: func(c *daemon.Clients) { gz = &gateZFS{Client: c.ZFS}; c.ZFS = gz }})
	e.Seed()
	c := ctxT(t)
	mustJail(t, e, "web")
	hit, release := gz.arm()
	defer release()
	snap, err := e.Admin.Jails().Snapshots("web").Create(c, "slow", "")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-hit:
	case <-time.After(10 * time.Second):
		t.Fatal("the snapshot never started")
	}
	// The jail is locked: a second operation on it is busy, others are not.
	_, err = e.Admin.Jails().Start(c, "web")
	wantCode(t, err, api.CodeBusy)
	_, err = e.Admin.Jails().Delete(c, "web")
	wantCode(t, err, api.CodeBusy)
	if _, err := e.Admin.Networks().Get(c, "lan"); err != nil {
		t.Fatal(err)
	}
	got, _ := e.Admin.Operations().Get(c, snap.ID())
	if got.Status != api.OpRunning || !got.MayCancel {
		t.Fatalf("%+v", got)
	}
	// Wait with a timeout returns the record while it is still running.
	start := time.Now()
	wc, cancel := context.WithTimeout(c, time.Second)
	defer cancel()
	_ = wc
	raw, body := e.Raw("GET", "/api/v1/operations/"+snap.ID()+"/wait?timeout=1", e.Token(api.PermAdmin), "")
	if raw != 200 || !strings.Contains(body, `"running"`) || time.Since(start) < 900*time.Millisecond {
		t.Fatalf("%d %s after %v", raw, body, time.Since(start))
	}
	if err := snap.Cancel(c); err != nil {
		t.Fatal(err)
	}
	_, err = snap.Wait(c)
	if !errors.Is(err, sdk.ErrCancelled) {
		t.Fatalf("%v", err)
	}
	final, _ := e.Admin.Operations().Get(c, snap.ID())
	if final.Status != api.OpCancelled || final.MayCancel {
		t.Fatalf("%+v", final)
	}
	// The lock is gone and nothing was left behind.
	for s, err := range e.Admin.Jails().Snapshots("web").List(c) {
		t.Fatalf("a cancelled snapshot exists: %v %v", s, err)
	}
	op, err := e.Admin.Jails().Start(c, "web")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := op.Wait(c); err != nil {
		t.Fatal(err)
	}
}
