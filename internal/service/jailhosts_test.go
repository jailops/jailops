package service_test

import (
	"context"
	"strings"
	"testing"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/internal/render"
	"jailops.net/jailops/sdk/api"
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

// An /etc/hosts the helper does not read (larger than it reads, a link out of the root) is left as it is: merging the
// block into nothing would leave a file without localhost.
func TestJailHostsThatCannotBeReadIsKept(t *testing.T) {
	e := dtest.New(t, dtest.Options{})
	e.Seed()
	s := e.D.Service()
	createJail(t, e, s, "web")
	hosts := "/jailops/projects/alpha/jails/web/root/etc/hosts"
	own := "127.0.0.1 localhost\n" + strings.Repeat("0.0.0.0 ads.example\n", 3)
	if err := e.Files.WriteAtomic(context.Background(), hosts, []byte(own)); err != nil {
		t.Fatal(err)
	}
	e.Files.ReadErr = map[string]error{hosts: api.Errorf(api.CodeForbidden, "file larger than 4 MiB")}
	jailAction(t, s, "web", "stop")
	jailAction(t, s, "web", "start")
	if got := e.File(hosts); got != own {
		t.Fatalf("an /etc/hosts that could not be read was rewritten:\n%s", got)
	}
}
