package service

import (
	"context"
	"testing"
	"time"

	"jailops.net/jailops/sdk/api"
)

func TestAnOperationWaitsForReconcileButNotForAPerson(t *testing.T) {
	l := newLockSet()
	rel, _ := l.tryLock("reconcile lan", "network/lan")
	go func() { time.Sleep(150 * time.Millisecond); rel() }()
	start := time.Now()
	r2, err := l.lockPatient(context.Background(), "create jail", "network/lan")
	if err != nil || time.Since(start) < 100*time.Millisecond {
		t.Fatalf("did not wait for the reconcile pass: %v", err)
	}
	// held by a person's operation: busy at once
	if _, err := l.lockPatient(context.Background(), "other", "network/lan"); !api.IsCode(err, api.CodeBusy) {
		t.Fatalf("%v", err)
	}
	r2()
}
