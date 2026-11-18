package service

import (
	"context"
	"testing"

	"jailops.net/jailops/sdk/api"
)

// Once Close has begun it is the last word: an operation or a background shell setup that is asked for afterwards (the
// reconcile loop of the daemon may still have a start in flight) starts nothing, so nothing of the service writes into the
// state after the daemon has closed it.
func TestNothingStartsAfterClose(t *testing.T) {
	s := newSvc(t)
	s.Close()

	ran := false
	_, err := s.Submit(bg, OpSpec{Description: "late", Locks: []string{"jail/x"}, Run: func(context.Context, *OpHandle) (any, error) {
		ran = true
		return nil, nil
	}})
	if !api.IsCode(err, api.CodeBusy) {
		t.Fatalf("an operation after Close: %v", err)
	}
	if ran {
		t.Fatal("an operation ran after Close")
	}
	if s.locks.isHeld("jail/x") {
		t.Fatal("the refused operation kept its lock")
	}

	s.setUpShellInBackground("p/x", "p", "x")
	s.shellM.mu.Lock()
	left := len(s.shellM.bg) + len(s.shellM.prog)
	s.shellM.mu.Unlock()
	if left != 0 {
		t.Fatalf("a shell setup was registered after Close: %d entries", left)
	}
}
