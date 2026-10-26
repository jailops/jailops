package validate

import (
	"testing"

	"jailops.net/jailops/sdk/api"
)

func TestUpdatePolicyShellAndProvisionValidation(t *testing.T) {
	base := func() api.JailSpec { return api.JailSpec{Image: "fb@stable"} }
	ok := base()
	ok.Update = api.UpdatePolicy{Mode: api.UpdateModeManaged, Schedule: "0 3 * * 0", Packages: true, Reboot: api.RebootAuto}
	ok.Shell = "/usr/local/bin/bash"
	ok.Provision = &api.Provision{Packages: []string{"bash", "nginx"},
		Files:    []api.ProvisionFile{{Source: "templates/nginx.conf", Destination: "/usr/local/etc/nginx/nginx.conf", Owner: "root", Group: "wheel", Mode: "0644"}},
		Services: api.ProvisionSvc{Enable: []string{"nginx"}, Start: []string{"nginx"}}, Commands: [][]string{{"/usr/bin/true"}}}
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
		"pkg flag":        func(s *api.JailSpec) { s.Provision = &api.Provision{Packages: []string{"-y"}} },
		"src absolute": func(s *api.JailSpec) {
			s.Provision = &api.Provision{Files: []api.ProvisionFile{{Source: "/etc/passwd", Destination: "/x"}}}
		},
		"src dotdot": func(s *api.JailSpec) {
			s.Provision = &api.Provision{Files: []api.ProvisionFile{{Source: "../../etc/passwd", Destination: "/x"}}}
		},
		"dest relative": func(s *api.JailSpec) {
			s.Provision = &api.Provision{Files: []api.ProvisionFile{{Source: "a", Destination: "x"}}}
		},
		"owner injection": func(s *api.JailSpec) {
			s.Provision = &api.Provision{Files: []api.ProvisionFile{{Source: "a", Destination: "/x", Owner: "root; id"}}}
		},
		"file mode": func(s *api.JailSpec) {
			s.Provision = &api.Provision{Files: []api.ProvisionFile{{Source: "a", Destination: "/x", Mode: "u+s"}}}
		},
		"service": func(s *api.JailSpec) {
			s.Provision = &api.Provision{Services: api.ProvisionSvc{Enable: []string{"a b"}}}
		},
		"empty command": func(s *api.JailSpec) { s.Provision = &api.Provision{Commands: [][]string{{}}} },
	}
	for name, mut := range bad {
		s := base()
		mut(&s)
		if err := JailSpec("web", s); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestHostMountValidation(t *testing.T) {
	spec := func(m ...api.HostMount) api.JailSpec { return api.JailSpec{Image: "fb@stable", HostMounts: m} }
	if err := JailSpec("web", spec(api.HostMount{Source: "/tank/media", Target: "/mnt/media", ReadOnly: true})); err != nil {
		t.Fatal(err)
	}
	for name, s := range map[string]api.JailSpec{
		"relative source":   spec(api.HostMount{Source: "tank/media", Target: "/mnt/media"}),
		"relative target":   spec(api.HostMount{Source: "/tank/media", Target: "mnt"}),
		"dot dot":           spec(api.HostMount{Source: "/tank/../etc", Target: "/mnt/x"}),
		"onto the root":     spec(api.HostMount{Source: "/tank/media", Target: "/"}),
		"two on one target": spec(api.HostMount{Source: "/tank/a", Target: "/mnt/x"}, api.HostMount{Source: "/tank/b", Target: "/mnt/x"}),
		"over a volume": {Image: "fb@stable", Volumes: []api.VolumeMount{{Name: "data", Mount: "/mnt/x"}},
			HostMounts: []api.HostMount{{Source: "/tank/a", Target: "/mnt/x"}}},
	} {
		if err := JailSpec("web", s); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
