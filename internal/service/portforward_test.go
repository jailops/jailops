package service_test

import (
	"context"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"testing"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/internal/service"
	"jailops.net/jailops/internal/state"
	"jailops.net/jailops/sdk/api"
)

// natLan gives the seeded network an uplink and NAT: its IPv4 addresses are reached by a forward of the host's ports.
func natLan(t *testing.T, s *service.Service) {
	t.Helper()
	root := as(globalAdmin, "")
	lan, err := s.GetNetwork(root, "lan")
	if err != nil {
		t.Fatal(err)
	}
	lan.Spec.Uplink = "em0"
	lan.Spec.IPv4.NAT = true
	if _, err := s.UpdateNetwork(root, "lan", lan, lan.Metadata.Generation); err != nil {
		t.Fatal(err)
	}
}

// seedJail creates a jail of the seeded project on the seeded network and waits for it.
func seedJail(t *testing.T, s *service.Service, name string) {
	t.Helper()
	op, err := s.CreateJail(as(globalAdmin, dtest.SeedProject), dtest.WebJail(name))
	if err != nil {
		t.Fatal(err)
	}
	if got := wait(t, s, op); got.Status != api.OpSuccess {
		t.Fatalf("create %s: %+v", name, got)
	}
}

// An IPv4 forward takes traffic that would reach the host: only a global administrator makes one, and never on a port
// the host listens on or on an address the host does not have.
func TestExposeForwardsAreGlobalAndKeepOffTheHostsOwnPorts(t *testing.T) {
	_, s := newEnv(t)
	natLan(t, s)
	seedJail(t, s, "web")
	project := as(alphaOnly, dtest.SeedProject)
	admin := as(globalAdmin, dtest.SeedProject)
	_, err := s.ExposeJail(project, "web", api.ExposeRequest{Ports: []string{"8081"}})
	wantCode(t, err, api.CodeForbidden)
	if err == nil || !strings.Contains(err.Error(), "global administrator") {
		t.Errorf("the refusal should say a global administrator: %v", err)
	}
	if ae, ok := err.(*api.Error); !ok || !strings.Contains(ae.Hint, "--family 6") {
		t.Errorf("the refusal should point at --family 6: %v", err)
	}
	for _, c := range []struct {
		port, listen, want string
	}{
		{"22", "", "ssh"},
		{"39110", "", "console"},
		{"39111", "", "backup node"},
		{"2224", "198.51.100.77", "not an address of this host"},
		{"2224", "2001:db8::77", "listen"},
	} {
		_, err := s.ExposeJail(admin, "web", api.ExposeRequest{Ports: []string{c.port}, Listen: c.listen})
		wantCode(t, err, api.CodeInvalid)
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("port %s on %q: %v, want %q", c.port, c.listen, err, c.want)
		}
	}
	// a host port other than the jail's port is checked, not the jail's port
	if _, err := s.ExposeJail(admin, "web", api.ExposeRequest{Ports: []string{"2222"}, HostPort: 39110}); err == nil || !strings.Contains(err.Error(), "console") {
		t.Errorf("a forward to a host port the host needs: %v", err)
	}
	// 22 inside the jail is fine when the host port is another one
	if _, err := s.ExposeJail(admin, "web", api.ExposeRequest{Ports: []string{"22"}, HostPort: 2224}); err != nil {
		t.Fatalf("a forward of a free host port to the jail's ssh: %v", err)
	}
	// an address the host has is allowed
	var own string
	if as, _ := net.InterfaceAddrs(); as != nil {
		for _, a := range as {
			if p, err := netip.ParsePrefix(a.String()); err == nil && p.Addr().Is4() && !p.Addr().IsLoopback() {
				own = p.Addr().String()
				break
			}
		}
	}
	if own != "" {
		if _, err := s.ExposeJail(admin, "web", api.ExposeRequest{Ports: []string{"8443"}, Listen: own}); err != nil {
			t.Fatalf("a forward on %s, an address of the host: %v", own, err)
		}
	}
}

// What a project administrator may not make they may not take away: the entries with a forward stay when they change
// the others, and removing a forward is a global administrator's.
func TestUnexposeOfAForwardIsGlobal(t *testing.T) {
	_, s := newEnv(t)
	natLan(t, s)
	seedJail(t, s, "web")
	project := as(alphaOnly, dtest.SeedProject)
	admin := as(globalAdmin, dtest.SeedProject)
	if _, err := s.ExposeJail(admin, "web", api.ExposeRequest{Ports: []string{"80", "443"}}); err != nil {
		t.Fatal(err)
	}
	_, err := s.UnexposeJail(project, "web", api.UnexposeRequest{Ports: []string{"80"}})
	wantCode(t, err, api.CodeForbidden)
	if r, err := s.UnexposeJail(admin, "web", api.UnexposeRequest{Ports: []string{"80"}}); err != nil || len(r.Entries) != 1 || r.Entries[0].Ports[0] != "443" {
		t.Fatalf("a global administrator took 80 out: %+v %v", r, err)
	}
}

// A jail without a forward (a network with no NAT) is exposed by a project administrator: nothing of the host is opened.
func TestExposeWithoutAForwardIsTheProjectAdminsOwn(t *testing.T) {
	_, s := newEnv(t) // the seeded network has no uplink: nothing arrives from outside, and nothing is forwarded
	seedJail(t, s, "web")
	project := as(alphaOnly, dtest.SeedProject)
	r, err := s.ExposeJail(project, "web", api.ExposeRequest{Ports: []string{"80", "443"}, Comment: "web"})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Changed || len(r.Entries) != 1 || len(r.Families) != 2 || r.Families[1].Via != "none" || r.Families[0].Note == "" {
		t.Fatalf("%+v", r)
	}
	if _, err := s.ExposeJail(project, "web", api.ExposeRequest{Ports: []string{"80"}, HostPort: 8080}); err == nil {
		t.Error("--host-port without a forward was accepted")
	}
	// the same request again changes nothing
	if r, err := s.ExposeJail(project, "web", api.ExposeRequest{Ports: []string{"80", "443"}, Comment: "web"}); err != nil || r.Changed {
		t.Fatalf("expose twice: %+v %v", r, err)
	}
	// dry run stores nothing
	if r, err := s.ExposeJail(project, "web", api.ExposeRequest{Ports: []string{"25"}, DryRun: true}); err != nil || !r.Changed {
		t.Fatalf("dry run: %+v %v", r, err)
	}
	j, _ := s.GetJail(project, "web")
	if len(j.Spec.Expose) != 1 {
		t.Fatalf("a dry run stored an entry: %+v", j.Spec.Expose)
	}
	if r, err := s.UnexposeJail(project, "web", api.UnexposeRequest{}); err != nil || len(r.Entries) != 0 {
		t.Fatalf("unexpose all: %+v %v", r, err)
	}
}

// The forwards that the state store took out of a policy and a profile at load become entries of the exposure of the
// jails that used them, once: the same host ports stay forwarded and the jails that did not use them gain nothing.
func TestMigratedForwardsBecomeTheExposureOfTheJailsThatHadThem(t *testing.T) {
	_, s := newEnv(t)
	natLan(t, s)
	admin := as(globalAdmin, dtest.SeedProject)
	seedJail(t, s, "uses-policy")
	seedJail(t, s, "uses-profile")
	seedJail(t, s, "plain")
	if _, err := s.CreatePolicy(admin, api.FirewallPolicy{Metadata: api.ObjectMeta{Name: "web"}, Spec: api.FirewallPolicySpec{DefaultIn: "deny", DefaultOut: "allow"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateProfile(admin, api.Profile{Metadata: api.ObjectMeta{Name: "pub"}, Spec: api.ProfileSpec{Config: map[string]string{"limits.pids": "300"}}}); err != nil {
		t.Fatal(err)
	}
	j, _ := s.GetJail(admin, "uses-policy")
	j.Spec.Firewall.Policies = []string{"web"}
	if _, err := s.UpdateJail(admin, "uses-policy", j, j.Metadata.Generation); err != nil {
		t.Fatal(err)
	}
	j, _ = s.GetJail(admin, "uses-profile")
	j.Spec.Profiles = []string{"pub"}
	if _, err := s.UpdateJail(admin, "uses-profile", j, j.Metadata.Generation); err != nil {
		t.Fatal(err)
	}
	fws := []state.MigratedForward{
		{Project: dtest.SeedProject, Policy: "web", Rule: 2, Proto: "tcp", HostPort: 8080, JailPort: 80, Comment: "site"},
		{Project: dtest.SeedProject, Profile: "pub", Device: "dns", Proto: "udp", HostPort: 53, JailPort: 53},
	}
	for range 2 { // a second time adds nothing
		if err := s.ApplyForwards(context.Background(), fws); err != nil {
			t.Fatal(err)
		}
	}
	check := func(name string, want ...string) {
		t.Helper()
		j, err := s.GetJail(admin, name)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, e := range j.Spec.Expose {
			got = append(got, e.Protocol()+" "+e.Ports[0]+" "+strconv.Itoa(e.HostPort)+" "+strings.Join(e.Families, ","))
		}
		if strings.Join(got, ";") != strings.Join(want, ";") {
			t.Errorf("%s: exposure %v, want %v", name, got, want)
		}
	}
	check("uses-policy", "tcp 80 8080 4")
	check("uses-profile", "udp 53 0 4")
	check("plain")
	j, _ = s.GetJail(admin, "uses-policy")
	if !strings.Contains(j.Spec.Expose[0].Comment, "policy web") {
		t.Errorf("the entry does not say where it came from: %+v", j.Spec.Expose[0])
	}
}
