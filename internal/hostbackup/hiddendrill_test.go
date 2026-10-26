package hostbackup

import (
	"strings"
	"testing"

	"jailops.net/jailops/internal/backup"
	"jailops.net/jailops/internal/validate"
)

// A full restore test without a runner restores the backup as a jail of its own, boots it without a network,
// checks it from the inside and removes it.
func TestFullTestRestoresAHiddenJailAndRemovesIt(t *testing.T) {
	_, nc := nodeServer(t)
	r := newRig(t, rigOpts{node: &nc})
	j := r.jail("web1", "uuid-web1", nil)
	bk := r.backup(j).Targets[0].Backup

	run, err := r.svc.RunDrill(bg, bk.Metadata.Name, backup.DepthFull)
	if err != nil || !run.OK {
		t.Fatalf("run = %+v, %v", run, err)
	}
	h := r.host
	if len(h.adopted) != 1 || !strings.HasPrefix(h.adopted[0].Jail.Metadata.Name, "jodrill-") {
		t.Fatalf("the restored jail: %+v", h.adopted)
	}
	hidden := h.adopted[0].Jail
	if err := validate.JailName(hidden.Metadata.Name); err != nil { // the name has to be one the daemon accepts
		t.Fatal(err)
	}
	if len(hidden.Spec.Network) != 0 {
		t.Fatalf("the test jail has network interfaces: %+v", hidden.Spec.Network)
	}
	if len(h.netMaps) != 0 {
		t.Fatalf("a test restore created networks: %v", h.netMaps)
	}
	if len(h.started) != 1 || h.started[0] != hidden.Metadata.Name {
		t.Fatalf("started %v", h.started)
	}
	if len(h.execs) != 1 || !strings.Contains(h.execs[0], "/bin/sh -c exit 0") {
		t.Fatalf("health check %v", h.execs)
	}
	if len(h.stopped) != 1 || len(h.deleted) != 1 || h.deleted[0] != hidden.Metadata.Name {
		t.Fatalf("cleanup: stopped %v deleted %v", h.stopped, h.deleted)
	}
}

func TestFullTestFailsAndCleansUpWhenTheHealthCheckFails(t *testing.T) {
	_, nc := nodeServer(t)
	r := newRig(t, rigOpts{node: &nc, cfg: func(c *Config) { c.HealthTimeout = 4 * 1e9 }})
	j := r.jail("web1", "uuid-web1", nil)
	bk := r.backup(j).Targets[0].Backup
	r.host.execCodes = []int{1, 1, 1, 1, 1, 1}
	run, err := r.svc.RunDrill(bg, bk.Metadata.Name, backup.DepthFull)
	if err == nil || run.OK || !strings.Contains(err.Error(), "health check failed") || !strings.Contains(err.Error(), "service is down") {
		t.Fatalf("run = %+v, %v", run, err)
	}
	if len(r.host.stopped) != 1 || len(r.host.deleted) != 1 {
		t.Fatalf("a failed test must still clean up: stopped %v deleted %v", r.host.stopped, r.host.deleted)
	}
	if len(r.host.execs) < 2 {
		t.Fatalf("the check was not retried: %v", r.host.execs)
	}
}

func TestFullTestUsesTheJailsOwnHealthCheck(t *testing.T) {
	_, nc := nodeServer(t)
	r := newRig(t, rigOpts{node: &nc})
	j := r.jail("web1", "uuid-web1", nil)
	j.Spec.Update.Healthcheck = "/usr/local/bin/check-web"
	bk := r.backup(j).Targets[0].Backup
	_, err := r.svc.RunDrill(bg, bk.Metadata.Name, backup.DepthFull)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.host.execs) != 1 || !strings.Contains(r.host.execs[0], "check-web") {
		t.Fatalf("execs %v", r.host.execs)
	}
}
