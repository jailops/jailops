package service_test

import (
	"path/filepath"
	"strings"
	"testing"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/sdk/api"
)

// Two jails that forward the same host port leave the second dead without a word, so the second is refused when it is
// made, naming the first.
func TestTwoJailsCannotForwardTheSameHostPort(t *testing.T) {
	_, s := newEnv(t)
	admin := as(globalAdmin, dtest.SeedProject)
	natLan(t, s)
	seedJail(t, s, "one")
	seedJail(t, s, "two")
	if _, err := s.ExposeJail(admin, "one", api.ExposeRequest{Ports: []string{"80"}, HostPort: 2301}); err != nil {
		t.Fatal(err)
	}
	_, err := s.ExposeJail(admin, "two", api.ExposeRequest{Ports: []string{"80"}, HostPort: 2301})
	wantCode(t, err, api.CodeAlreadyExists)
	if err == nil || !strings.Contains(err.Error(), `"one"`) {
		t.Fatalf("the refusal should name the first jail: %v", err)
	}
	// another host port is fine, and so is the same jail port
	if _, err := s.ExposeJail(admin, "two", api.ExposeRequest{Ports: []string{"80"}, HostPort: 2302}); err != nil {
		t.Fatal(err)
	}
	// the same host port on another protocol is another port
	if _, err := s.ExposeJail(admin, "two", api.ExposeRequest{Proto: "udp", Ports: []string{"53"}, HostPort: 2301}); err != nil {
		t.Fatal(err)
	}
	// a plain port of another jail is the same host port as far as the host is concerned
	seedJail(t, s, "three")
	if _, err := s.ExposeJail(admin, "three", api.ExposeRequest{Ports: []string{"2301"}}); err == nil || !strings.Contains(err.Error(), `"one"`) {
		t.Fatalf("a plain port that is another jail's host port: %v", err)
	}
	if _, err := s.ExposeJail(admin, "three", api.ExposeRequest{Ports: []string{"2303"}}); err != nil {
		t.Fatal(err)
	}
}

// A jail with a link and no address (a network entry of "none") has a rendered anchor that closes both directions, and
// so has a jail with an address: no jail with an interface is left without one.
func TestEveryJailWithALinkHasARenderedAnchor(t *testing.T) {
	e, s := newEnv(t)
	admin := as(globalAdmin, dtest.SeedProject)
	for _, c := range []struct {
		name, ipv4 string
		closed     bool
	}{{"addressed", "", false}, {"nowhere", "none", true}} {
		j := dtest.WebJail(c.name)
		j.Spec.Network = []api.NetworkAttach{{Network: "lan", IPv4: c.ipv4}}
		op, err := s.CreateJail(admin, j)
		if err != nil {
			t.Fatal(err)
		}
		if got := wait(t, s, op); got.Status != api.OpSuccess {
			t.Fatalf("create %s: %+v", c.name, got)
		}
		jailAction(t, s, c.name, "start")
		text := e.File(filepath.Join(e.Cfg.StateDir, "rendered", "pf", dtest.SeedProject+"_"+c.name+".conf"))
		if text == "" {
			t.Fatalf("%s: no rendered anchor", c.name)
		}
		closed := strings.Contains(text, "the jail has no address on its links") &&
			strings.Contains(text, "block drop in quick on ") && !strings.Contains(text, "pass")
		if closed != c.closed {
			t.Errorf("%s: closed anchor %v, want %v:\n%s", c.name, closed, c.closed, text)
		}
	}
}
