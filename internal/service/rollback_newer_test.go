package service_test

import (
	"testing"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/sdk/api"
)

// destroy_newer destroys snapshots: it is accepted only together with confirm_data_loss, before any work starts.
func TestRollbackDestroyNewerNeedsTheConfirmation(t *testing.T) {
	_, s := newEnv(t)
	seedJail(t, s, "web")
	admin := as(globalAdmin, dtest.SeedProject)
	for _, scope := range []api.RollbackScope{api.RollbackRoot, api.RollbackAll} {
		_, err := s.RollbackJail(admin, "web", api.JailRollback{Scope: scope, Snapshot: "s1", DestroyNewer: true})
		wantCode(t, err, api.CodeDataLoss)
	}
}
