package service_test

import (
	"slices"
	"testing"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/sdk/api"
)

// An operation lists the steps it ran and how long each took, so that a slow start or stop explains itself in
// `jailops op show`.
func TestOperationListsItsSteps(t *testing.T) {
	e, s := tickingEnv(t)
	alpha := as(globalAdmin, dtest.SeedProject)
	op, err := s.CreateJail(alpha, dtest.WebJail("web"))
	if err != nil {
		t.Fatal(err)
	}
	if got := wait(t, s, op); got.Status != api.OpSuccess {
		t.Fatalf("create: %+v\n%s", got, e.Log())
	}
	for _, action := range []string{"start", "stop"} {
		op, err := s.JailAction(alpha, "web", api.JailAction{Action: action})
		if err != nil {
			t.Fatal(err)
		}
		got := wait(t, s, op)
		if got.Status != api.OpSuccess {
			t.Fatalf("%s: %+v\n%s", action, got, e.Log())
		}
		if len(got.Steps) == 0 {
			t.Fatalf("%s: the operation lists no steps: %+v", action, got)
		}
		if action == "stop" {
			names := []string{}
			for _, st := range got.Steps {
				names = append(names, st.Name)
			}
			for _, want := range []string{"jail-stop", "teardown", "unmount"} {
				if !slices.Contains(names, want) {
					t.Errorf("stop: step %q missing from %v", want, names)
				}
			}
		}
	}
}
