package rollout

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"jailops.net/jailops/internal/fleet/config"
	"jailops.net/jailops/internal/fleet/model"
	"jailops.net/jailops/internal/fleet/plan"
	"jailops.net/jailops/sdk/api"
)

func TestStoreRoundTripIsAtomic(t *testing.T) {
	s := Store{Dir: t.TempDir() + "/nested"}
	if _, err := s.Load("x"); !errors.Is(err, ErrNoState) {
		t.Fatalf("got %v", err)
	}
	st := &State{Name: "x", Status: StatusRunning, Items: []*Item{{Host: "h", Jail: "j", State: ItemDone}, {State: ItemPending}}, Batches: [][2]int{{0, 1}, {1, 2}}}
	if err := s.Save(st); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load("x")
	if err != nil || len(got.Items) != 2 || got.Batches[1] != [2]int{1, 2} {
		t.Fatalf("%+v %v", got, err)
	}
	if c := got.Counts(); c[ItemDone] != 1 || c[ItemPending] != 1 {
		t.Fatalf("%v", c)
	}
	if err := s.Remove("x"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load("x"); !errors.Is(err, ErrNoState) {
		t.Fatal("not removed")
	}
}

func TestHealthy(t *testing.T) {
	ok := api.JailStatus{Power: api.PowerRunning, Generation: "g2"}
	if h, _ := healthy(ok, "g2"); !h {
		t.Fatal("a running jail on the target is healthy")
	}
	for name, st := range map[string]api.JailStatus{
		"stopped":   {Power: api.PowerStopped, Generation: "g2"},
		"old gen":   {Power: api.PowerRunning, Generation: "g1"},
		"not ready": {Power: api.PowerRunning, Generation: "g2", Conditions: []api.Condition{{Type: "Ready", Status: api.ConditionFalse, Since: time.Now()}}},
		"unknown":   {Power: api.PowerRunning, Generation: "g2", Conditions: []api.Condition{{Type: "Healthy", Status: api.ConditionUnknown}}},
	} {
		if h, why := healthy(st, "g2"); h || why == "" {
			t.Errorf("%s: healthy=%v why=%q", name, h, why)
		}
	}
	other := api.JailStatus{Power: api.PowerRunning, Generation: "g2", Conditions: []api.Condition{{Type: "Backup", Status: api.ConditionFalse}}}
	if h, _ := healthy(other, "g2"); !h {
		t.Fatal("unrelated conditions must not gate")
	}
}

func executor(t *testing.T, rollouts map[string]*config.Rollout) (*Executor, Store) {
	t.Helper()
	f := &config.Fleet{Meta: model.Meta{ID: "f1"}, Hosts: map[string]*config.Host{}, Rollouts: rollouts}
	store := Store{Dir: t.TempDir()}
	return &Executor{Session: plan.NewSession(f, nil, nil), Store: store, Out: io.Discard}, store
}

func TestRunRefusesWhatIsNotSafeToStart(t *testing.T) {
	r := &config.Rollout{Name: "web", Spec: model.RolloutSpec{}}
	e, store := executor(t, map[string]*config.Rollout{"web": r})
	ctx := context.Background()
	if _, err := e.Run(ctx, "ghost", Start); err == nil || !strings.Contains(err.Error(), "unknown rollout") {
		t.Fatalf("%v", err)
	}
	if _, err := e.Run(ctx, "web", Resume); err == nil || !strings.Contains(err.Error(), "nothing to resume") {
		t.Fatalf("%v", err)
	}
	// an unfinished rollout is neither silently restarted nor resumed after the spec changed
	st := &State{Name: "web", Status: StatusFailed, SpecHash: specHash(r), Items: []*Item{{State: ItemDone}, {State: ItemFailed}}}
	if err := store.Save(st); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Run(ctx, "web", Start); err == nil || !strings.Contains(err.Error(), "rollout resume") {
		t.Fatalf("a failed rollout was restarted without being asked: %v", err)
	}
	st.SpecHash = "stale"
	if err := store.Save(st); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Run(ctx, "web", Resume); err == nil || !strings.Contains(err.Error(), "spec changed") {
		t.Fatalf("a changed spec was resumed: %v", err)
	}
	// a finished rollout resumes to itself
	st.Status, st.SpecHash = StatusDone, specHash(r)
	if err := store.Save(st); err != nil {
		t.Fatal(err)
	}
	got, err := e.Run(ctx, "web", Resume)
	if err != nil || got.Status != StatusDone {
		t.Fatalf("%v %+v", err, got)
	}
}

func TestAbort(t *testing.T) {
	e, store := executor(t, nil)
	ctx := context.Background()
	if _, err := e.Abort(ctx, "none"); !errors.Is(err, ErrNoState) {
		t.Fatalf("%v", err)
	}
	st := &State{Name: "web", Status: StatusRunning, Items: []*Item{{State: ItemDone}, {State: ItemPending}}}
	if err := store.Save(st); err != nil {
		t.Fatal(err)
	}
	got, err := e.Abort(ctx, "web")
	if err != nil || got.Status != StatusAborted || got.Error == "" {
		t.Fatalf("%v %+v", err, got)
	}
	if again, _ := store.Load("web"); again.Status != StatusAborted {
		t.Fatalf("the abort was not saved: %+v", again)
	}
	st.Status = StatusDone
	if err := store.Save(st); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Abort(ctx, "web"); err == nil {
		t.Fatal("a finished rollout was aborted")
	}
}

func TestSleepStopsWithTheContext(t *testing.T) {
	e := &Executor{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
	if err := e.sleep(context.Background(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
}
