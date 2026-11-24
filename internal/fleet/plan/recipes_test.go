package plan

import (
	"testing"

	"jailops.net/jailops/sdk/api"
)

func TestClassifyJudgesOnlyWhatItCanRead(t *testing.T) {
	step := func(kind, status string) api.ApplyStep { return api.ApplyStep{Kind: kind, Name: "x", Status: status} }
	for name, tc := range map[string]struct {
		steps []api.ApplyStep
		want  RecipeStatus
	}{
		"all unchanged":                 {[]api.ApplyStep{step("package", api.StepUnchanged), step("file", api.StepUnchanged)}, StatusConverged},
		"a file would change":           {[]api.ApplyStep{step("file", api.StepWouldChange)}, StatusDrift},
		"a dir with a wrong mode":       {[]api.ApplyStep{step("dir", api.StepWouldChange), step("service", api.StepUnchanged)}, StatusDrift},
		"a run with creates would run":  {[]api.ApplyStep{step("run", api.StepWouldChange)}, StatusDrift},
		"a run whose creates exists":    {[]api.ApplyStep{step("run", api.StepSkipped)}, StatusConverged},
		"an unguarded run is an action": {[]api.ApplyStep{step("file", api.StepUnchanged), step("run", api.StepWouldExecute)}, StatusConverged},
		"unless is an action":           {[]api.ApplyStep{step("run", api.StepWouldCheck)}, StatusConverged},
		"drift behind actions":          {[]api.ApplyStep{step("run", api.StepWouldExecute), step("sysrc", api.StepWouldChange)}, StatusDrift},
	} {
		if got := Classify(tc.steps); got != tc.want {
			t.Errorf("%s: %s, want %s", name, got, tc.want)
		}
	}
}
