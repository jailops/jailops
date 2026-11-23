package service

import (
	"errors"
	"strings"
	"testing"

	"jailops.net/jailops/internal/lifecycle"
	"jailops.net/jailops/internal/plan"
	"jailops.net/jailops/sdk/api"
)

// A failed in-place upgrade of a thick jail says what failed and what is left: the jail runs as the upgrade left it, the
// snapshot is kept for a manual rollback; nothing is rolled back on its own.
func TestThickHealthFailureIsPublic(t *testing.T) {
	he := &lifecycle.HealthError{Msg: "health check failed", Command: "/bin/check", Output: "connection refused", Err: errors.New("exit 1")}
	pe := &plan.Error{Plan: "upgrade-jail web", Step: "health-check", Err: he}
	thick := &lifecycle.UpgradeResult{Snapshot: "jo-pre-upgrade-100"}
	err := thickHealthFailure(pe, thick)
	var pub *publicError
	if !errors.As(err, &pub) || !strings.Contains(err.Error(), "the health check failed: /bin/check: exit 1: connection refused; the jail runs as the upgrade left it") || !strings.Contains(err.Error(), "jo-pre-upgrade-100 is kept") {
		t.Fatalf("%v", err)
	}
	if !errors.As(err, &pe) || !errors.As(err, &he) {
		t.Fatal("the plan error is lost from the chain")
	}
}

// A step that fails says which step and what it said; a typed error keeps its code; a failure before the plan runs
// goes through unchanged; a failure before the snapshot says nothing changed.
func TestThickPlanFailureIsPublic(t *testing.T) {
	thick := &lifecycle.UpgradeResult{Snapshot: "jo-pre-upgrade-100"}
	pe := &plan.Error{Plan: "upgrade-jail web", Step: "pkg-upgrade", Err: errors.New("pkg: No space left on device")}
	err := thickHealthFailure(pe, thick)
	if !strings.Contains(err.Error(), "failed at step pkg-upgrade") || !strings.Contains(err.Error(), "No space left") || !strings.Contains(err.Error(), "jo-pre-upgrade-100 is kept") {
		t.Fatalf("%v", err)
	}
	typed := &plan.Error{Plan: "p", Step: "verify-release", Err: api.Errorf(api.CodeInvalid, "the base packages are version 15.2.b3")}
	if got := thickHealthFailure(typed, thick); !api.IsCode(got, api.CodeInvalid) || !strings.Contains(got.Error(), "runs as the upgrade left it") {
		t.Fatalf("%v", got)
	}
	plain := errors.New("planning failed")
	if thickHealthFailure(plain, thick) != plain {
		t.Fatal("a failure before the plan ran was changed")
	}
	early := &plan.Error{Plan: "p", Step: "check-scripts", Err: errors.New("unsupported")}
	if got := thickHealthFailure(early, thick); !strings.Contains(got.Error(), "nothing in the jail was changed") {
		t.Fatalf("%v", got)
	}
}

// The history of a failed thick upgrade: a failure after the snapshot is a failure left as it is (never "rolled back":
// nothing rolls back on its own), one before it changed nothing, and a suspected root is said so.
func TestUpgradeOutcome(t *testing.T) {
	thick := &lifecycle.UpgradeResult{Snapshot: "jo-pre-upgrade-100"}
	deps := &plan.Error{Plan: "p", Step: "verify-deps", Err: api.Errorf(api.CodePreconditionFailed, "the package dependencies of the upgraded root are not complete")}
	err := thickHealthFailure(deps, thick)
	if r, note := upgradeOutcome(err, "15.1-RELEASE-p7"); r != api.UpdateFailure || !strings.Contains(note, "runs as the upgrade left it") {
		t.Fatalf("verify-deps: %s %s", r, note)
	}
	if !api.IsCode(err, api.CodePreconditionFailed) {
		t.Fatalf("the code is lost: %v", err)
	}
	early := &plan.Error{Plan: "p", Step: "prefetch", Err: errors.New("fetch: no route")}
	if r, _ := upgradeOutcome(thickHealthFailure(early, thick), "x"); r != api.UpdatePreflightFailed {
		t.Fatalf("prefetch: %s", r)
	}
	if r, _ := upgradeOutcome(api.Errorf(api.CodeVersionSkew, "newer than the kernel"), "x"); r != api.UpdatePreflightFailed {
		t.Fatalf("a refused check: %s", r)
	}
	se := &lifecycle.SuspectedError{Jail: "web", Found: "etc/crontab is a symbolic link out of the root"}
	if r, note := upgradeOutcome(se, "x"); r != api.UpdatePreflightFailed || !strings.HasPrefix(note, "suspected_compromised: etc/crontab") {
		t.Fatalf("suspected: %s %s", r, note)
	}
}
