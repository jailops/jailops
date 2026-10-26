package e2e

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jailops.net/jailops/internal/fleet/fleettest"
	"jailops.net/jailops/internal/fleet/plan"
	"jailops.net/jailops/internal/fleet/rollout"
	"jailops.net/jailops/sdk/api"
)

// rolloutFleet applies three web jails (a, b, c) on one host and then makes a
// newer image generation available.
func rolloutFleet(t *testing.T, running ...string) *fleettest.Env {
	t.Helper()
	e := fleettest.New(t, "testfleet", "web1")
	e.StandardObjects()
	for _, n := range []string{"a", "b", "c"} {
		e.Jail("web1", n, "tier: web", "")
	}
	e.Write("rollouts/web-upgrade.yaml", `apiVersion: jailops/v1
kind: Rollout
metadata:
  name: web-upgrade
spec:
  project: main
  selector: {tier: web}
  canary: 1
  batch_size: 1
  pause: 1ms
  health_gate: {timeout: 150ms, interval: 5ms}
`)
	mustOK(t, run(t, e, "sync", "--apply"))
	srv := e.Hosts["web1"].Server
	srv.SeedGeneration("freebsd-15")
	srv.SeedGeneration("freebsd-15")
	c := client(t, e.Hosts["web1"]).Project(fleettest.Project)
	for _, n := range running {
		op, err := c.Jails().Start(context.Background(), n)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := op.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

func upgradeCalls(e *fleettest.Env, jail string) int {
	n := 0
	for _, rq := range e.Hosts["web1"].Server.Requests() {
		if rq.Method == "POST" && rq.Path == "/1.0/jails/"+jail+"/upgrade" {
			n++
		}
	}
	return n
}

func TestRolloutRunsInBatches(t *testing.T) {
	e := rolloutFleet(t, "a", "b", "c")
	r := mustOK(t, run(t, e, "rollout", "run", "web-upgrade"))
	contains(t, r.out, "batch 1/3")
	contains(t, r.out, "batch 3/3")
	contains(t, r.out, "rollout web-upgrade: done")
	for _, n := range []string{"a", "b", "c"} {
		if g := getJail(t, e.Hosts["web1"], n).Status.Generation; g != "g2" {
			t.Fatalf("%s is on %s", n, g)
		}
	}
	// Done rollouts can run again (nothing to upgrade, all already on target).
	mustOK(t, run(t, e, "rollout", "run", "web-upgrade"))
	r = mustOK(t, run(t, e, "rollout", "list"))
	contains(t, r.out, "web-upgrade")
}

func TestRolloutAbortsOnHealthGateFailureAndResumes(t *testing.T) {
	// c is never started, so the gate after its batch cannot pass.
	e := rolloutFleet(t, "a", "b")
	r := mustFail(t, run(t, e, "rollout", "run", "web-upgrade"), "health gate failed")
	contains(t, r.out, "rollout web-upgrade: failed")
	contains(t, r.out, "power is stopped")
	if strings.Contains(r.out, "batch 4") {
		t.Fatal("ran past the last batch")
	}
	for _, n := range []string{"a", "b"} {
		if g := getJail(t, e.Hosts["web1"], n).Status.Generation; g != "g2" {
			t.Fatalf("%s is on %s", n, g)
		}
	}
	// A failed rollout is not silently restarted.
	mustFail(t, run(t, e, "rollout", "run", "web-upgrade"), "rollout resume")
	st := mustOK(t, run(t, e, "rollout", "status", "web-upgrade"))
	contains(t, st.out, "web1/c g1 -> g2: unhealthy")

	// Fix the jail, resume: only c is looked at, and c is not upgraded twice.
	c := client(t, e.Hosts["web1"]).Project(fleettest.Project)
	op, _ := c.Jails().Start(context.Background(), "c")
	if _, err := op.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	r = mustOK(t, run(t, e, "rollout", "resume", "web-upgrade"))
	contains(t, r.out, "rollout web-upgrade: done")
	for _, n := range []string{"a", "b", "c"} {
		if got := upgradeCalls(e, n); got != 1 {
			t.Fatalf("jail %s was upgraded %d times, want 1", n, got)
		}
	}
	// The apply log names the rollout events.
	logs, _ := filepath.Glob(filepath.Join(e.State, "apply-*.log"))
	if len(logs) < 2 {
		t.Fatalf("expected a log per run: %v", logs)
	}
}

func TestRolloutCanaryFailureLeavesTheRestUntouched(t *testing.T) {
	e := rolloutFleet(t, "a", "b", "c")
	e.Hosts["web1"].Server.FailOperation("jails.upgrade", api.Errorf(api.CodeInternal, "boom"))
	r := mustFail(t, run(t, e, "rollout", "run", "web-upgrade"), "boom")
	contains(t, r.out, "rollout web-upgrade: failed")
	for _, n := range []string{"b", "c"} {
		if g := getJail(t, e.Hosts["web1"], n).Status.Generation; g != "g1" {
			t.Fatalf("%s was touched after the canary failed: %s", n, g)
		}
		if got := upgradeCalls(e, n); got != 0 {
			t.Fatalf("%s: %d upgrade calls", n, got)
		}
	}
	// Resume retries the canary (the injected failure is spent) and finishes.
	mustOK(t, run(t, e, "rollout", "resume", "web-upgrade"))
	if g := getJail(t, e.Hosts["web1"], "c").Status.Generation; g != "g2" {
		t.Fatalf("c is on %s", g)
	}
	// --restart discards an unfinished state; resume of a finished one is a no-op.
	mustOK(t, run(t, e, "rollout", "resume", "web-upgrade"))
}

func TestRolloutOnlyTouchesOwnedJails(t *testing.T) {
	e := rolloutFleet(t, "a", "b", "c")
	e.Hosts["web1"].Server.SeedJail(api.Jail{
		Metadata: api.ObjectMeta{Name: "stranger", Project: fleettest.Project, Labels: map[string]string{"tier": "web"}},
		Spec:     api.JailSpec{Image: "freebsd-15@stable"},
		Status:   api.JailStatus{Power: api.PowerRunning, Generation: "g1"},
	})
	r := mustOK(t, run(t, e, "rollout", "run", "web-upgrade"))
	contains(t, r.out, "skip web1/stranger: not owned by this fleet")
	if g := getJail(t, e.Hosts["web1"], "stranger").Status.Generation; g != "g1" {
		t.Fatalf("a foreign jail was upgraded: %s", g)
	}
	mustFail(t, run(t, e, "rollout", "run", "nope"), "unknown rollout")
}

// A rollout interrupted while an upgrade runs is resumed by waiting for that
// same operation, not by issuing a second upgrade.
func TestRolloutResumeAttachesToRunningOperation(t *testing.T) {
	e := rolloutFleet(t, "a", "b", "c")
	e.Hosts["web1"].Server.SetOperationDelay(300 * time.Millisecond)

	f := loadFleet(t, e)
	conn := &plan.SDKConnector{Fleet: f, Store: nil}
	sess := plan.NewSession(f, conn, nil)
	defer sess.Close()
	ex := &rollout.Executor{Session: sess, Store: rollout.Store{Dir: filepath.Join(e.State, "rollouts", "testfleet")}}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// Cancel once the first upgrade has been requested.
		for i := 0; i < 400; i++ {
			if upgradeCalls(e, "a") > 0 {
				cancel()
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
	}()
	st, err := ex.Run(ctx, "web-upgrade", rollout.Start)
	if err == nil {
		t.Fatal("the interrupted rollout reported success")
	}
	if st.Status != rollout.StatusAborted || st.Items[0].State != rollout.ItemUpgrading || st.Items[0].Op == "" {
		t.Fatalf("state after interruption: %s %+v", st.Status, st.Items[0])
	}
	op := st.Items[0].Op

	e.Hosts["web1"].Server.SetOperationDelay(0)
	st, err = ex.Run(context.Background(), "web-upgrade", rollout.Resume)
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != rollout.StatusDone {
		t.Fatalf("status %s", st.Status)
	}
	if got := upgradeCalls(e, "a"); got != 1 {
		t.Fatalf("jail a upgraded %d times; resume must wait for operation %s", got, op)
	}
}
