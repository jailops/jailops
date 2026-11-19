package service

import (
	"time"

	"errors"
	"jailops.net/jailops/internal/lifecycle"
	"testing"

	"jailops.net/jailops/internal/plan"
	"jailops.net/jailops/sdk/api"
)

func TestUpgradeOutcome(t *testing.T) {
	undone := &plan.Error{Plan: "upgrade", Step: "start", Err: errors.New("jail did not start")}
	stuck := &plan.Error{Plan: "upgrade", Step: "start", Err: errors.New("jail did not start"), UndoErrs: []error{errors.New("zfs rename: busy")}}
	merge := api.Errorf(api.CodePreconditionFailed, "merging /etc has 2 conflicting files")
	for _, tc := range []struct {
		name       string
		err        error
		result     api.UpdateResult
		rolledBack bool
	}{
		{"every undo step ran", undone, api.UpdateRolledBack, true},
		{"an undo step failed", stuck, api.UpdateRecoveryNeeded, false},
		{"the merge needs a human", merge, api.UpdatePreflightFailed, false},
		{"the plan was never started", errors.New("image is not built"), api.UpdatePreflightFailed, false},
	} {
		got, rb, note := upgradeOutcome(tc.err, "freebsd-15-1#g1")
		if got != tc.result || rb != tc.rolledBack || note == "" {
			t.Errorf("%s: %q, rolled back %v, note %q; want %q, %v", tc.name, got, rb, note, tc.result, tc.rolledBack)
		}
	}
}

func TestThePreviousRootOfAnUpgradedJailIsKept(t *testing.T) {
	s := newSvc(t)
	if got := s.lcfg.RootOldRetention; got != lifecycle.DefaultRootOldRetention || got < 7*24*time.Hour {
		t.Fatalf("root.old is kept for %v, want %v: housekeeping would destroy the previous root at its next pass", got, lifecycle.DefaultRootOldRetention)
	}
}
