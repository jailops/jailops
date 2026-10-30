package daemon_test

import (
	"slices"
	"strings"
	"testing"

	"jailops.net/jailops/internal/daemon"
)

// host.manage.modules is empty by default and accepts only known modules, each once.
func TestHostManageConfig(t *testing.T) {
	cfg, err := daemon.Decode(strings.NewReader("pool: tank\n"))
	if err != nil || len(cfg.Host.Manage.Modules) != 0 {
		t.Fatalf("%v %v", err, cfg.Host.Manage.Modules)
	}
	cfg, err = daemon.Decode(strings.NewReader("pool: tank\nhost:\n  manage:\n    modules: [identity]\n"))
	if err != nil || !slices.Equal(cfg.Host.Manage.Modules, []string{"identity"}) {
		t.Fatalf("%v %v", err, cfg.Host.Manage.Modules)
	}
	for in, want := range map[string]string{
		"pool: tank\nhost:\n  manage:\n    modules: [firewall]\n":           `"firewall" is not a module`,
		"pool: tank\nhost:\n  manage:\n    modules: [identity, identity]\n": "twice",
		"pool: tank\nhost:\n  manage: identity\n":                           "HostManage",
		"pool: tank\nhost:\n  modules: [identity]\n":                        "modules",
	} {
		if _, err := daemon.Decode(strings.NewReader(in)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v", in, err)
		}
	}
}
