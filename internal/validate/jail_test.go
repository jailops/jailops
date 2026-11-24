package validate

import (
	"testing"

	"jailops.net/jailops/sdk/api"
)

func TestUpdatePolicyAndShellValidation(t *testing.T) {
	base := func() api.JailSpec { return api.JailSpec{Image: "fb"} }
	ok := base()
	ok.Update = api.UpdatePolicy{Mode: api.UpdateModeManaged, Schedule: "0 3 * * 0", Packages: true, Reboot: api.RebootAuto}
	ok.Shell = "/usr/local/bin/bash"
	if err := JailSpec("web", ok); err != nil {
		t.Fatal(err)
	}
	bad := map[string]func(*api.JailSpec){
		"mode":            func(s *api.JailSpec) { s.Update.Mode = "auto" },
		"notify no sched": func(s *api.JailSpec) { s.Update = api.UpdatePolicy{Mode: api.UpdateModeNotify} },
		"managed nothing": func(s *api.JailSpec) { s.Update = api.UpdatePolicy{Mode: api.UpdateModeManaged, Schedule: "0 3 * * 0"} },
		"bad cron":        func(s *api.JailSpec) { s.Update.Schedule = "every day" },
		"reboot":          func(s *api.JailSpec) { s.Update.Reboot = "sometimes" },
		"shell relative":  func(s *api.JailSpec) { s.Shell = "bash" },
		"shell space":     func(s *api.JailSpec) { s.Shell = "/bin/sh -l" },
	}
	for name, mut := range bad {
		s := base()
		mut(&s)
		if err := JailSpec("web", s); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
