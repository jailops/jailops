package service_test

import (
	"strings"
	"testing"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/internal/render"
)

// A FreeBSD jail's own name resolves to its own addresses through a block of /etc/hosts that JailOps owns; the lines
// the jail has around it stay.
func TestJailHostsHasTheNameOfTheJail(t *testing.T) {
	e := dtest.New(t, dtest.Options{})
	e.Seed()
	s := e.D.Service()
	createJail(t, e, s, "web")
	hosts := "/jailops/projects/alpha/jails/web/root/etc/hosts"
	got := e.File(hosts)
	if !strings.Contains(got, render.HostsBegin) || !strings.Contains(got, "\tweb\n") || !strings.Contains(got, render.HostsEnd) {
		t.Fatalf("the block of the jail:\n%s", got)
	}
	jailAction(t, s, "web", "stop")
	jailAction(t, s, "web", "start")
	if again := e.File(hosts); strings.Count(again, render.HostsBegin) != 1 {
		t.Fatalf("a restart keeps one block:\n%s", again)
	}
}
