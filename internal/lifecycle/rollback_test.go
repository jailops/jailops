package lifecycle

import (
	"errors"
	"strings"
	"testing"

	"jailops.net/jailops/internal/lifecycle/fault"
	"jailops.net/jailops/sdk/api"
)

func rollback(w *world, o RollbackOptions) (RollbackResult, error) {
	if o.Stamp == "" {
		o.Stamp = "300"
	}
	return w.S.RollbackJail(bg, "alpha_web", o, false)
}

// snapWorld is a jail with a snapshot s1 of its root, written to after it, and stopped (a rollback refuses a running
// jail).
func snapWorld(t *testing.T) *world {
	w := runningSetup(t)
	w.Z.WriteFile(jc+"/root", "/home/a", "v1")
	w.Z.WriteFile(dbVol, "/db/x", "v1")
	if _, err := w.S.SnapshotJail(bg, "alpha_web", "s1", ""); err != nil {
		t.Fatal(err)
	}
	w.Z.WriteFile(jc+"/root", "/home/a", "version-two")
	w.Z.WriteFile(jc+"/root", "/home/new", "n")
	w.Z.WriteFile(dbVol, "/db/x", "v2")
	if err := w.S.StopJail(bg, "alpha_web"); err != nil {
		t.Fatal(err)
	}
	return w
}

// A rollback of any scope refuses a running jail, and JailOps does not stop it: the administrator does.
func TestRollbackRefusesARunningJail(t *testing.T) {
	w := snapWorld(t)
	if err := w.S.StartJail(bg, "alpha_web"); err != nil {
		t.Fatal(err)
	}
	before := w.state()
	for _, scope := range []api.RollbackScope{api.RollbackData, api.RollbackAll} {
		_, err := rollback(w, RollbackOptions{JailRollback: api.JailRollback{Scope: scope, Snapshot: "s1", ConfirmDataLoss: true}})
		wantCode(t, err, api.CodePreconditionFailed)
	}
	if w.state() != before || !w.Jails.IsUp("alpha_web") {
		t.Fatal("a refused rollback changed the jail")
	}
}

func TestRollbackErrors(t *testing.T) {
	w := runningSetup(t)
	_, err := rollback(w, RollbackOptions{JailRollback: api.JailRollback{Snapshot: "x"}})
	wantCode(t, err, api.CodeInvalid)
	_, err = rollback(w, RollbackOptions{JailRollback: api.JailRollback{Scope: "weird"}})
	wantCode(t, err, api.CodeInvalid)
	_, err = rollback(w, RollbackOptions{JailRollback: api.JailRollback{Scope: api.RollbackData}})
	wantCode(t, err, api.CodeInvalid)
	_, err = rollback(w, RollbackOptions{JailRollback: api.JailRollback{Scope: api.RollbackAll, Snapshot: "nope", ConfirmDataLoss: true}})
	wantCode(t, err, api.CodeNotFound)
}

// Scope all returns the root to a snapshot: it needs the loss confirmed, refuses newer snapshots without force, and
// never touches a volume.
func TestRollbackAllReturnsTheRootToASnapshot(t *testing.T) {
	w := snapWorld(t)
	opts := RollbackOptions{JailRollback: api.JailRollback{Scope: api.RollbackAll, Snapshot: "s1"}}
	before := w.state()
	res, err := rollback(w, opts)
	var ae *api.Error
	if !errors.As(err, &ae) || ae.Code != api.CodeDataLoss || !strings.Contains(ae.Detail["datasets"], jc+"/root=") {
		t.Fatalf("want data_loss_unconfirmed, got %v", err)
	}
	if res.TotalWritten != int64(len("version-two")+len("n")) || w.state() != before {
		t.Fatalf("written %d, or the refusal changed the pool", res.TotalWritten)
	}
	w.S.SnapshotJail(bg, "alpha_web", "s2", "")
	opts.ConfirmDataLoss = true
	before = w.state()
	if _, err := rollback(w, opts); !errors.As(err, &ae) || ae.Code != api.CodePreconditionFailed || !strings.Contains(ae.Detail["later_snapshots"], jc+"/root@s2") {
		t.Fatalf("newer snapshots must refuse without force: %v", err)
	}
	if w.state() != before {
		t.Fatal("refusal changed state")
	}
	opts.Force = true
	if _, err := rollback(w, opts); err != nil {
		t.Fatal(err)
	}
	if v, _ := w.Z.ReadFile(jc+"/root", "/home/a"); v != "v1" {
		t.Fatal(v)
	}
	if _, ok := w.Z.ReadFile(jc+"/root", "/home/new"); ok {
		t.Fatal("new file survived")
	}
	if v, _ := w.Z.ReadFile(dbVol, "/db/x"); v != "v2" {
		t.Fatalf("a rollback of the jail must not touch its volume, got %q", v)
	}
	if ok, _ := w.Z.Exists(bg, jc+"/root@s2"); ok {
		t.Fatal("later snapshot should be destroyed by force")
	}
	if w.Jails.IsUp("alpha_web") {
		t.Fatal("the rollback started the jail")
	}
}

// Scope data restores the dataset the jail manages itself: a jail without one has nothing to roll back, and its root
// is not touched.
func TestRollbackDataLeavesTheRoot(t *testing.T) {
	w := snapWorld(t)
	if _, err := rollback(w, RollbackOptions{JailRollback: api.JailRollback{Scope: api.RollbackData, Snapshot: "s1", ConfirmDataLoss: true}}); err != nil {
		t.Fatal(err)
	}
	if v, _ := w.Z.ReadFile(jc+"/root", "/home/a"); v != "version-two" {
		t.Fatalf("a data rollback touched the root: %q", v)
	}
}

func TestRollbackAllFailureIsRetryable(t *testing.T) {
	w := snapWorld(t)
	w.F.Reset(0)
	opts := RollbackOptions{JailRollback: api.JailRollback{Scope: api.RollbackAll, Snapshot: "s1", ConfirmDataLoss: true}}
	if _, err := rollback(w, opts); err != nil {
		t.Fatal(err)
	}
	n := w.F.Count()
	for k := 1; k <= n; k++ {
		w := snapWorld(t)
		w.F.Reset(k)
		_, err := rollback(w, opts)
		if err == nil {
			continue
		}
		if !errors.Is(err, fault.ErrInjected) {
			t.Fatal(err)
		}
		if ok, _ := w.Z.Exists(bg, jc+"/root@s1"); !ok {
			t.Fatalf("op %d lost the snapshot", k)
		}
		w.F.Reset(0)
		if _, err := rollback(w, opts); err != nil {
			t.Fatalf("retry after failure %d (%s): %v", k, w.F.Log, err)
		}
		if v, _ := w.Z.ReadFile(jc+"/root", "/home/a"); v != "v1" {
			t.Fatalf("op %d: retry did not complete", k)
		}
		if err := w.S.StartJail(bg, "alpha_web"); err != nil {
			t.Fatalf("op %d: jail does not start after retry: %v", k, err)
		}
	}
}

func TestRollbackDryRun(t *testing.T) {
	w := snapWorld(t)
	before := w.state()
	w.F.Reset(0)
	res, err := w.S.RollbackJail(bg, "alpha_web", RollbackOptions{JailRollback: api.JailRollback{Scope: api.RollbackAll, Snapshot: "s1", ConfirmDataLoss: true}}, true)
	if err != nil || len(res.Plan) == 0 || w.F.Count() != 0 || w.state() != before {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(res.Plan, "\n"), "zfs rollback -r "+jc+"/root@s1") {
		t.Fatal(res.Plan)
	}
}
