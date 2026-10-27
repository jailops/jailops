package server_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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

// crashZFS fails mutating calls from the nth Rename on, as a dead process
// would leave them: the undo of the running plan cannot run either.
type crashZFS struct {
	zfs.Client
	armed atomic.Bool
	dead  atomic.Bool
	n     atomic.Int32
	after int32
}

func (z *crashZFS) gate() error {
	if z.dead.Load() {
		return errors.New("process died")
	}
	return nil
}

func (z *crashZFS) Rename(ctx context.Context, from, to string, o zfs.RenameOpts) error {
	if err := z.gate(); err != nil {
		return err
	}
	if z.armed.Load() && z.n.Add(1) == z.after {
		z.dead.Store(true)
		return errors.New("process died")
	}
	return z.Client.Rename(ctx, from, to, o)
}
func (z *crashZFS) Create(ctx context.Context, o zfs.CreateOpts) error {
	if err := z.gate(); err != nil {
		return err
	}
	return z.Client.Create(ctx, o)
}
func (z *crashZFS) Clone(ctx context.Context, s, t string, p map[string]string, par bool) error {
	if err := z.gate(); err != nil {
		return err
	}
	return z.Client.Clone(ctx, s, t, p, par)
}
func (z *crashZFS) Snapshot(ctx context.Context, o zfs.SnapshotOpts) error {
	if err := z.gate(); err != nil {
		return err
	}
	return z.Client.Snapshot(ctx, o)
}
func (z *crashZFS) Destroy(ctx context.Context, n string, o zfs.DestroyOpts) error {
	if err := z.gate(); err != nil {
		return err
	}
	return z.Client.Destroy(ctx, n, o)
}
func (z *crashZFS) Set(ctx context.Context, n string, p map[string]string) error {
	if err := z.gate(); err != nil {
		return err
	}
	return z.Client.Set(ctx, n, p)
}

func TestRestartRecoversInterruptedUpgrade(t *testing.T) {
	var cz *crashZFS
	e := dtest.New(t, dtest.Options{Clients: func(c *daemon.Clients) { cz = &crashZFS{Client: c.ZFS, after: 2}; c.ZFS = cz }})
	e.Seed()
	c := ctxT(t)
	mustJail(t, e, "web")
	bop, _ := e.Admin.Images().Build(c, "fb", api.ImageBuild{FromGeneration: 1})
	if _, err := bop.Wait(c); err != nil {
		t.Fatal(err)
	}
	before, _ := e.ZFS.Get(c, "tank/jailops/projects/alpha/jails/web/root", "jailops:gen")
	if before["jailops:gen"].Value != "1" {
		t.Fatalf("%v", before)
	}
	// The second rename of the plan is the swap of root and root.new: the
	// process "dies" there, with the journal on disk.
	cz.armed.Store(true)
	up, err := e.Admin.Jails().Upgrade(c, "web", api.JailUpgrade{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = up.Wait(c)
	if err == nil {
		t.Fatal("the upgrade survived the crash")
	}
	opID := up.ID()
	jdir := filepath.Join(e.Dir, "data", "journal")
	ents, _ := os.ReadDir(jdir)
	if len(ents) == 0 {
		t.Fatal("no journal left by the interrupted upgrade")
	}
	state := e.ZFS
	_ = state
	e.Stop()
	cz.dead.Store(false)
	cz.armed.Store(false)

	// Restart on the same state: recovery rolls the plan back.
	e2 := dtest.New(t, dtest.Options{Reuse: e, Clients: func(c *daemon.Clients) { c.ZFS = cz }})
	got, err := e2.Admin.Operations().Get(c, opID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != api.OpFailure || got.Error == nil {
		t.Fatalf("%+v", got)
	}
	if rec := got.Error.Detail["recovery"]; !strings.HasPrefix(rec, "rolled-back") {
		t.Fatalf("recovery detail %q (error %v)", rec, got.Error)
	}
	ents, _ = os.ReadDir(jdir)
	if len(ents) != 0 {
		var names []string
		for _, e := range ents {
			names = append(names, e.Name())
		}
		t.Fatalf("journal not cleaned: %v", names)
	}
	// The jail is on its old generation, with no leftovers, and starts.
	j, err := e2.Admin.Jails().Get(c, "web")
	if err != nil || j.Status.Generation != "fb#g1" {
		t.Fatalf("%v %+v\n%s", err, j.Status, e2.Log())
	}
	for _, ds := range []string{"tank/jailops/projects/alpha/jails/web/root.new", "tank/jailops/projects/alpha/jails/web/root.old"} {
		if ok, _ := e.ZFS.Exists(c, ds); ok {
			t.Errorf("%s was left behind", ds)
		}
	}
	gen, _ := e.ZFS.Get(c, "tank/jailops/projects/alpha/jails/web/root", "jailops:gen")
	if gen["jailops:gen"].Value != "1" {
		t.Fatalf("root generation %v", gen)
	}
	so, err := e2.Admin.Jails().Start(c, "web")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := so.Wait(c); err != nil {
		t.Fatalf("start after recovery: %v\n%s", err, e2.Log())
	}
	rep := e2.Admin.Operations()
	_ = rep
	doc, err := e2.Admin.Doctor(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range doc.Checks {
		if ch.Name == "operations.journal" && ch.Status != api.DoctorOK {
			t.Fatalf("%+v", ch)
		}
	}
	// And the upgrade can be retried.
	up2, err := e2.Admin.Jails().Stop(c, "web")
	if err != nil {
		t.Fatal(err)
	}
	up2.Wait(c)
	retry, err := e2.Admin.Jails().Upgrade(c, "web", api.JailUpgrade{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := retry.Wait(c); err != nil {
		t.Fatalf("retry: %v\n%s", err, e2.Log())
	}
}
