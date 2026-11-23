package service

import (
	"context"
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
)

// A jail whose upgrade failed and whose rollback failed too is reported on the jail (a condition with the command) and by
// doctor (a failing row per jail, none otherwise).
func TestUpgradeIncompleteIsReported(t *testing.T) {
	s := cpusetService(t, "4", nil, pinnedJail("web", ""), pinnedJail("db", ""))
	c := &checkSet{}
	s.doctorUpgradeState(c)
	if len(c.list) != 1 || c.list[0].Status != api.DoctorOK || c.list[0].Name != "jail.upgrade_state" {
		t.Fatalf("%+v", c.list)
	}
	s.mu.Lock()
	s.jails["alpha_web"].incomplete = "upgrade-jail failed at step pkg-upgrade and the rollback did not finish: zfs rollback: busy"
	e := s.jails["alpha_web"]
	s.mu.Unlock()
	c = &checkSet{}
	s.doctorUpgradeState(c)
	if len(c.list) != 1 || c.list[0].Status != api.DoctorFail || !strings.Contains(c.list[0].Hint, "jailops jail rollback web --project alpha --confirm-data-loss") {
		t.Fatalf("%+v", c.list)
	}
	j := s.view(context.Background(), e, map[string]int{})
	found := false
	for _, cd := range j.Status.Conditions {
		if cd.Type == "UpgradeIncomplete" && cd.Status == api.ConditionTrue && strings.Contains(cd.Message, "jailops jail rollback web --project alpha --confirm-data-loss") {
			found = true
		}
	}
	if !found {
		t.Fatalf("%+v", j.Status.Conditions)
	}
}
