package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"jailops.net/jailops/internal/daemon"
	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/internal/sys/zfs"
	"jailops.net/jailops/sdk/api"
)

// leakyZFS fails every snapshot with what a real zfs failure holds: the dataset and the host path.
type leakyZFS struct {
	zfs.Client
	armed *atomic.Bool
}

const (
	leakedDataset = "tank/jailops/projects/alpha/jails/web@leak-1"
	leakedPath    = "/usr/local/jailops/projects/alpha/jails/web/root"
)

func (l *leakyZFS) Snapshot(ctx context.Context, o zfs.SnapshotOpts) error {
	if l.armed.Load() {
		return errors.New("cannot create snapshot '" + leakedDataset + "': dataset busy (mounted at " + leakedPath + ")")
	}
	return l.Client.Snapshot(ctx, o)
}

// A failed operation says "internal error" and its id to every reader, in the operation, the list, the event stream
// and the audit trail; the text of the failure is in the daemon log under the id.
func TestFailedOperationDoesNotShowHostDetails(t *testing.T) {
	var armed atomic.Bool
	e := dtest.New(t, dtest.Options{Clients: func(c *daemon.Clients) { c.ZFS = &leakyZFS{Client: c.ZFS, armed: &armed} }})
	e.Seed()
	c := ctxT(t)
	mustJail(t, e, "web")
	evctx, cancel := context.WithCancel(c)
	defer cancel()
	events := e.Events(evctx, e.Admin)
	armed.Store(true)
	op, err := e.Admin.Jails().Snapshots("web").Create(c, "x", "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = op.Wait(c)
	armed.Store(false)
	var ae *api.Error
	if !errors.As(err, &ae) || ae.Code != api.CodeInternal || ae.Message != "internal error" ||
		!strings.Contains(ae.Hint, "daemon log") || !strings.Contains(ae.Hint, op.ID()) {
		t.Fatalf("%#v", err)
	}
	noLeak := func(what string, v any) {
		t.Helper()
		b, _ := json.Marshal(v)
		for _, s := range []string{leakedDataset, leakedPath, "dataset busy", "tank/jailops"} {
			if strings.Contains(string(b), s) {
				t.Fatalf("%s shows %q:\n%s", what, s, b)
			}
		}
	}
	noLeak("the error of Wait", ae)
	got, err := e.Admin.Operations().Get(c, op.ID())
	if err != nil {
		t.Fatal(err)
	}
	noLeak("the operation", got)
	if got.Status != api.OpFailure || got.Error == nil || got.Error.Message != "internal error" {
		t.Fatalf("%+v", got)
	}
	for o := range e.Admin.Operations().List(c) {
		noLeak("the list of operations", o)
	}
	// the event stream: the final state of this operation
	for final := false; !final; {
		select {
		case ev := <-events:
			if ev.Type != api.EventOperation {
				continue
			}
			noLeak("an operation event", ev)
			var o api.Operation
			if json.Unmarshal(ev.Metadata, &o) == nil && o.ID == op.ID() && o.Status.Final() {
				final = true
			}
		case <-c.Done():
			t.Fatal("no final event of the operation")
		}
	}
	entries, err := e.Admin.Audit(c, 50)
	if err != nil {
		t.Fatal(err)
	}
	noLeak("the audit trail", entries)
	// the daemon log has the text, under the operation id
	log := e.Log()
	for _, want := range []string{leakedDataset, leakedPath, op.ID()} {
		if !strings.Contains(log, want) {
			t.Fatalf("the daemon log lacks %q:\n%s", want, log)
		}
	}
}
