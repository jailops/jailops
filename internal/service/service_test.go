package service

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"jailops.net/jailops/internal/plan"
	"jailops.net/jailops/internal/state"
	"jailops.net/jailops/internal/store"
	"jailops.net/jailops/sdk/api"
)

var bg = context.Background()

func newSvc(t *testing.T) *Service {
	t.Helper()
	st, err := state.Open(t.TempDir(), state.Options{})
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Deps{State: st, DB: db, JournalDir: t.TempDir(), HostUUID: "h"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(); db.Close(); st.Close() })
	return s
}

func TestLocksAreAllOrNothing(t *testing.T) {
	l := newLockSet()
	un, err := l.tryLock("a", "jail/x", "network/n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.tryLock("b", "jail/y", "network/n"); !api.IsCode(err, api.CodeBusy) {
		t.Fatalf("%v", err)
	}
	if l.isHeld("jail/y") {
		t.Fatal("a failed attempt kept part of its locks")
	}
	un()
	un() // idempotent
	if _, err := l.tryLock("b", "jail/y", "network/n"); err != nil {
		t.Fatal(err)
	}
}

func TestMergePatch(t *testing.T) {
	type in struct {
		A string            `json:"a"`
		B map[string]string `json:"b"`
		C int               `json:"c"`
	}
	var out in
	err := MergePatch(in{A: "x", B: map[string]string{"k": "v", "z": "1"}, C: 3}, map[string]any{"b": map[string]any{"k": nil, "n": "2"}, "c": 4.0}, &out)
	if err != nil || out.A != "x" || out.C != 4 || len(out.B) != 2 || out.B["n"] != "2" || out.B["z"] != "1" {
		t.Fatalf("%v %+v", err, out)
	}
	if err := MergePatch(in{}, map[string]any{"c": "not a number"}, &out); !api.IsCode(err, api.CodeInvalid) {
		t.Fatalf("%v", err)
	}
}

func TestOperationLifecycleAndPanic(t *testing.T) {
	s := newSvc(t)
	op, err := s.Submit(bg, OpSpec{Description: "ok", Locks: []string{"jail/a"}, Run: func(ctx context.Context, h *OpHandle) (any, error) {
		h.Progress(50)
		return map[string]string{"x": "y"}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.WaitOperation(bg, op.ID, 5*time.Second)
	if got.Status != api.OpSuccess || got.Progress != 100 || string(got.Metadata) != `{"x":"y"}` || got.MayCancel {
		t.Fatalf("%+v", got)
	}
	if s.IsLocked("jail/a") {
		t.Fatal("lock leaked")
	}
	// A panic fails the operation, releases the lock and keeps the daemon alive.
	op, _ = s.Submit(bg, OpSpec{Description: "boom", Locks: []string{"jail/a"}, Run: func(context.Context, *OpHandle) (any, error) { panic("kaboom") }})
	got, _ = s.WaitOperation(bg, op.ID, 5*time.Second)
	if got.Status != api.OpFailure || got.Error == nil || got.Error.Code != api.CodeInternal {
		t.Fatalf("%+v", got)
	}
	if s.IsLocked("jail/a") {
		t.Fatal("lock leaked after a panic")
	}
	// An error keeps its code; a plan error unwraps to the failing step's code.
	op, _ = s.Submit(bg, OpSpec{Description: "plan", Run: func(context.Context, *OpHandle) (any, error) {
		return nil, &plan.Error{Plan: "p", Step: "s", Err: api.Errorf(api.CodeNoQuorum, "x"), UndoErrs: []error{errors.New("u")}}
	}})
	got, _ = s.WaitOperation(bg, op.ID, 5*time.Second)
	if got.Error == nil || got.Error.Code != api.CodeNoQuorum || got.Error.Hint == "" {
		t.Fatalf("%+v", got.Error)
	}
	// Busy at submit time.
	release := make(chan struct{})
	op, _ = s.Submit(bg, OpSpec{Description: "hold", Locks: []string{"jail/b"}, Run: func(ctx context.Context, _ *OpHandle) (any, error) {
		<-release
		return nil, nil
	}})
	if _, err := s.Submit(bg, OpSpec{Description: "second", Locks: []string{"jail/b"}, Run: nil}); !api.IsCode(err, api.CodeBusy) {
		t.Fatalf("%v", err)
	}
	close(release)
	s.WaitOperation(bg, op.ID, 5*time.Second)
	// The finished operations are listed, newest first.
	l, err := s.ListOperations(bg)
	if err != nil || len(l) != 4 {
		t.Fatalf("%v %d", err, len(l))
	}
}

func TestInterruptedOperationsAreFailedAtStart(t *testing.T) {
	s := newSvc(t)
	// A record left "running" by a dead process.
	now := time.Now().UTC()
	if err := s.d.DB.Operations.Put(bg, api.Operation{ID: "dead-op", Description: "x", Status: api.OpRunning, MayCancel: true, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(bg); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetOperation(bg, "dead-op")
	if err != nil || got.Status != api.OpFailure || got.Error == nil || got.MayCancel {
		t.Fatalf("%v %+v", err, got)
	}
}

func TestEventsReplayAndSlowSubscriber(t *testing.T) {
	s := newSvc(t)
	for i := 0; i < 5; i++ {
		s.events.publish(api.EventLifecycle, map[string]int{"i": i}, time.Now())
	}
	bus := s.Events()
	ctx, cancel := context.WithTimeout(bg, 5*time.Second)
	defer cancel()
	ch := bus.Subscribe(ctx, 2) // ids 3..5 replayed
	var ids []int64
	for i := 0; i < 3; i++ {
		ids = append(ids, (<-ch).ID)
	}
	if ids[0] != 3 || ids[2] != 5 {
		t.Fatalf("%v", ids)
	}
	s.events.publish(api.EventLifecycle, 1, time.Now())
	if se := <-ch; se.ID != 6 {
		t.Fatalf("%d", se.ID)
	}
	// "From now" skips history.
	now := bus.Subscribe(ctx, -1)
	s.events.publish(api.EventLifecycle, 2, time.Now())
	if se := <-now; se.ID != 7 {
		t.Fatalf("%d", se.ID)
	}
	// A subscriber that never reads is cut off; publishers do not block.
	stuck := bus.Subscribe(ctx, -1)
	done := make(chan struct{})
	go func() {
		for i := 0; i < subscriberBuffer+200; i++ {
			s.events.publish(api.EventLifecycle, i, time.Now())
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("a slow subscriber blocked the publisher")
	}
	n := 0
	for range stuck {
		n++
		if n > 10000 {
			t.Fatal("stream never ended")
		}
	}
}

func TestFileJournalRoundTrip(t *testing.T) {
	j, err := newFileJournal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := plan.Record{ID: "upgrade/web", Plan: "p", Steps: []string{"a", "b"}, Begun: 2, Done: 1, State: plan.StateRunning}
	if err := j.Put(bg, r); err != nil {
		t.Fatal(err)
	}
	got, ok, err := j.Get(bg, "upgrade/web")
	if err != nil || !ok || got.Done != 1 || len(got.Steps) != 2 {
		t.Fatalf("%v %v %+v", err, ok, got)
	}
	_ = j.putRequest("upgrade/web", opRequest{OpID: "op1", Kind: "upgrade", Stamp: "9"})
	if req, ok := j.getRequest("upgrade/web"); !ok || req.OpID != "op1" || req.Stamp != "9" {
		t.Fatalf("%+v", req)
	}
	l, _ := j.List(bg)
	if len(l) != 1 || l[0].ID != "upgrade/web" {
		t.Fatalf("%+v", l)
	}
	_ = j.Delete(bg, "upgrade/web")
	if _, ok, _ := j.Get(bg, "upgrade/web"); ok {
		t.Fatal("not deleted")
	}
	if err := j.Delete(bg, "upgrade/web"); err != nil {
		t.Fatal("deleting twice failed")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = j.Put(bg, r) }()
	}
	wg.Wait()
}
