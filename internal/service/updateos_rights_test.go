package service_test

import (
	"strings"
	"testing"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/internal/service"
	"jailops.net/jailops/sdk/api"
)

// forbiddenScheduledUpgrade checks that err is the refusal of the scheduled in-place upgrade of a jail's base.
func forbiddenScheduledUpgrade(t *testing.T, what string, err error) {
	t.Helper()
	if !api.IsCode(err, api.CodeForbidden) || !strings.Contains(err.Error(), "update.os=true with update.mode=managed") || !strings.Contains(err.Error(), "global administrator") {
		t.Fatalf("%s: %v", what, err)
	}
}

// update.os=true with update.mode=managed has the host run pkg as root against the jail's root on the jail's schedule:
// only a global administrator turns it on, at create, update, patch and config set. The other update keys stay a
// project administrator's, turning the pair off is allowed, and a jail that has it keeps it while other keys change.
func TestScheduledBaseUpgradeIsAGlobalAdministrators(t *testing.T) {
	_, s := newEnv(t)
	admin := as(globalAdmin, dtest.SeedProject)
	proj := as(alphaOnly, dtest.SeedProject) // a project administrator, not a global one
	managedOS := api.UpdatePolicy{Mode: api.UpdateModeManaged, Schedule: "0 3 * * *", OS: true, Packages: true}

	// create
	j := dtest.WebJail("web")
	j.Spec.Update = managedOS
	_, err := s.CreateJail(proj, j)
	forbiddenScheduledUpgrade(t, "create by a project administrator", err)
	j.Spec.Update.OS = false
	op, err := s.CreateJail(proj, j)
	mustOp(t, s, op, err)

	// config set: update.os on a managed jail, and managed on a jail with update.os
	_, err = s.ConfigureJail(proj, "web", api.ConfigChange{Set: map[string]string{"update.os": "true"}}, false)
	forbiddenScheduledUpgrade(t, "config set update.os", err)
	op, err = s.ConfigureJail(proj, "web", api.ConfigChange{Set: map[string]string{"update.mode": "notify"}}, false)
	mustOp(t, s, op, err)
	op, err = s.ConfigureJail(proj, "web", api.ConfigChange{Set: map[string]string{"update.os": "true"}}, false)
	mustOp(t, s, op, err) // notify with os only reports
	_, err = s.ConfigureJail(proj, "web", api.ConfigChange{Set: map[string]string{"update.mode": "managed"}}, false)
	forbiddenScheduledUpgrade(t, "config set update.mode managed", err)

	// update (PUT) and patch
	cur, err := s.GetJail(proj, "web")
	if err != nil {
		t.Fatal(err)
	}
	cur.Spec.Update.Mode = api.UpdateModeManaged
	_, err = s.UpdateJail(proj, "web", cur, service.AnyGeneration)
	forbiddenScheduledUpgrade(t, "update", err)
	_, err = s.PatchJail(proj, "web", map[string]any{"spec": map[string]any{"update": map[string]any{"mode": "managed"}}}, service.AnyGeneration)
	forbiddenScheduledUpgrade(t, "patch", err)

	// a global administrator turns it on
	op, err = s.ConfigureJail(admin, "web", api.ConfigChange{Set: map[string]string{"update.mode": "managed"}}, false)
	mustOp(t, s, op, err)

	// the jail has it now: a project administrator changes the other update keys, and the jail keeps it
	op, err = s.ConfigureJail(proj, "web", api.ConfigChange{Set: map[string]string{"update.schedule": "0 4 * * *", "update.packages": "false"}}, false)
	mustOp(t, s, op, err)
	if _, err := s.PatchJail(proj, "web", map[string]any{"spec": map[string]any{"update": map[string]any{"healthcheck": "true"}}}, service.AnyGeneration); err != nil {
		t.Fatalf("patch of another update key: %v", err)
	}
	if got, _ := s.GetJail(proj, "web"); got.Spec.Update.Mode != api.UpdateModeManaged || !got.Spec.Update.OS {
		t.Fatalf("the policy changed: %+v", got.Spec.Update)
	}
	// turning it off is allowed; turning it on again is not
	op, err = s.ConfigureJail(proj, "web", api.ConfigChange{Set: map[string]string{"update.os": "false", "update.packages": "true"}}, false)
	mustOp(t, s, op, err)
	_, err = s.ConfigureJail(proj, "web", api.ConfigChange{Set: map[string]string{"update.os": "true"}}, false)
	forbiddenScheduledUpgrade(t, "turning it on again", err)

	// a global administrator creates a jail with it
	j2 := dtest.WebJail("web2")
	j2.Spec.Update = managedOS
	op, err = s.CreateJail(admin, j2)
	mustOp(t, s, op, err)
}
