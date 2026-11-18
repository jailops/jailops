package service_test

import (
	"context"
	"strings"
	"testing"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/internal/state"
	"jailops.net/jailops/sdk/api"
)

// A range of ports cannot be forwarded one to one: on a jail behind NAT it is refused, and nothing is stored.
func TestExposeOfARangeBehindNATIsRefused(t *testing.T) {
	_, s := newEnv(t)
	natLan(t, s)
	seedJail(t, s, "web")
	admin := as(globalAdmin, dtest.SeedProject)
	_, err := s.ExposeJail(admin, "web", api.ExposeRequest{Ports: []string{"8000-8100"}})
	wantCode(t, err, api.CodeInvalid)
	if !strings.Contains(err.Error(), "range") {
		t.Errorf("the refusal should name the range: %v", err)
	}
	if j, _ := s.GetJail(admin, "web"); len(j.Spec.Expose) != 0 {
		t.Errorf("a refused request stored an entry: %+v", j.Spec.Expose)
	}
}

// What a global administrator forwards, a project administrator cannot change through the jail's own spec either: not the
// sources, not the jail port, not remove it.
func TestAForwardIsChangedByAGlobalAdministratorOnly(t *testing.T) {
	_, s := newEnv(t)
	natLan(t, s)
	seedJail(t, s, "web")
	admin := as(globalAdmin, dtest.SeedProject)
	project := as(alphaOnly, dtest.SeedProject)
	if _, err := s.ExposeJail(admin, "web", api.ExposeRequest{Ports: []string{"80"}, From: []string{"192.0.2.0/24"}}); err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func(*api.Jail){
		"sources widened": func(j *api.Jail) { j.Spec.Expose[0].From = nil },
		"removed":         func(j *api.Jail) { j.Spec.Expose = nil },
		"host port moved": func(j *api.Jail) { j.Spec.Expose[0].HostPort = 8081 },
	} {
		j, _ := s.GetJail(project, "web")
		edit(&j)
		_, err := s.UpdateJail(project, "web", j, j.Metadata.Generation)
		if err == nil {
			t.Errorf("%s: a project administrator changed a forward", name)
			continue
		}
		wantCode(t, err, api.CodeForbidden)
	}
}

// A profile edit passes what an exposure of the jail itself passes: a jail that forwards a port the host listens on is not
// made valid by an edit of one of its profiles.
func TestAProfileEditDoesNotLetAForwardOfAHostPortThrough(t *testing.T) {
	_, s := newEnv(t)
	seedJail(t, s, "web")
	admin := as(globalAdmin, dtest.SeedProject)
	project := as(alphaOnly, dtest.SeedProject)
	if _, err := s.CreateProfile(admin, api.Profile{Metadata: api.ObjectMeta{Name: "pub"}, Spec: api.ProfileSpec{Config: map[string]string{"limits.pids": "300"}}}); err != nil {
		t.Fatal(err)
	}
	j, _ := s.GetJail(admin, "web")
	j.Spec.Profiles = []string{"pub"}
	j.Spec.Expose = []api.ExposeEntry{{Ports: []string{"22"}}} // no forward yet: the network has no NAT
	if _, err := s.UpdateJail(project, "web", j, j.Metadata.Generation); err != nil {
		t.Fatal(err)
	}
	natLan(t, s) // the network gets NAT: the entry is now a forward of host port 22
	p, _ := s.GetProfile(project, "pub")
	p.Spec.Config["limits.pids"] = "400"
	_, err := s.UpdateProfile(project, "pub", p, p.Metadata.Generation)
	if err == nil || !strings.Contains(err.Error(), "ssh") {
		t.Fatalf("a profile edit let a forward of host port 22 stand: %v", err)
	}
}

// The migration says what it did not make, and makes the rest: a rule that was switched off, a host port another jail
// already has, and sources that are jails or the host.
func TestMigratedForwardsReportWhatTheyDoNotMake(t *testing.T) {
	_, s := newEnv(t)
	natLan(t, s)
	admin := as(globalAdmin, dtest.SeedProject)
	seedJail(t, s, "a")
	seedJail(t, s, "b")
	if _, err := s.CreatePolicy(admin, api.FirewallPolicy{Metadata: api.ObjectMeta{Name: "shared"}, Spec: api.FirewallPolicySpec{DefaultIn: "deny", DefaultOut: "allow"}}); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"a", "b"} {
		j, _ := s.GetJail(admin, n)
		j.Spec.Firewall.Policies = []string{"shared"}
		if _, err := s.UpdateJail(admin, n, j, j.Metadata.Generation); err != nil {
			t.Fatal(err)
		}
	}
	fws := []state.MigratedForward{
		{Project: dtest.SeedProject, Policy: "shared", Rule: 1, Proto: "tcp", HostPort: 8080, JailPort: 80},
		{Project: dtest.SeedProject, Policy: "shared", Rule: 2, Proto: "tcp", HostPort: 9090, JailPort: 90, Disabled: true},
		{Project: dtest.SeedProject, Policy: "shared", Rule: 3, Proto: "tcp", HostPort: 7070, JailPort: 70, From: []string{"host"}},
		{Project: dtest.SeedProject, Policy: "shared", Rule: 4, Proto: "tcp", HostPort: 6060, JailPort: 60, From: []string{"192.0.2.0/24", "jail:a"}},
	}
	if err := s.ApplyForwards(context.Background(), fws); err != nil {
		t.Fatal(err)
	}
	ja, _ := s.GetJail(admin, "a")
	jb, _ := s.GetJail(admin, "b")
	var got []string
	for _, e := range ja.Spec.Expose {
		got = append(got, e.Ports[0]+":"+strings.Join(e.From, "+"))
	}
	if strings.Join(got, " ") != "80: 60:192.0.2.0/24" {
		t.Errorf("jail a exposes %v: the first jail keeps the forwards, the disabled and host-only ones are not made, the external source stays", got)
	}
	if len(jb.Spec.Expose) != 0 {
		t.Errorf("jail b takes the host ports of a: %+v", jb.Spec.Expose)
	}
}

// The upgrade note in the product: an allow in rule without a source on a routed jail no longer opens the Internet, and
// doctor lists the rule, so the operator sees what changed.
func TestDoctorListsAllowInRulesWithoutASource(t *testing.T) {
	_, s := newEnv(t)
	root := as(globalAdmin, "")
	lan, err := s.GetNetwork(root, "lan")
	if err != nil {
		t.Fatal(err)
	}
	lan.Spec.Uplink = "em0" // an uplink without NAT: the IPv4 addresses are routed
	if _, err := s.UpdateNetwork(root, "lan", lan, lan.Metadata.Generation); err != nil {
		t.Fatal(err)
	}
	admin := as(globalAdmin, dtest.SeedProject)
	if _, err := s.CreatePolicy(admin, api.FirewallPolicy{Metadata: api.ObjectMeta{Name: "site"}, Spec: api.FirewallPolicySpec{DefaultIn: "deny", DefaultOut: "allow",
		Rules: []api.FirewallRule{{Name: "http", Action: "allow", Direction: "in", Proto: "tcp", Ports: []string{"80"}}}}}); err != nil {
		t.Fatal(err)
	}
	op, err := s.CreateJail(admin, func() api.Jail {
		j := dtest.WebJail("web")
		j.Spec.Firewall.Policies = []string{"site"}
		return j
	}())
	if err != nil {
		t.Fatal(err)
	}
	wait(t, s, op)
	var found bool
	for _, c := range s.Doctor(root).Checks {
		if c.Name == "jails.inbound-without-source" {
			found = true
			if !strings.Contains(c.Message, "policy site rule 1") || !strings.Contains(c.Hint, "--from any") {
				t.Errorf("the finding does not name the rule or the way out: %+v", c)
			}
		}
	}
	if !found {
		t.Error("doctor does not list the allow in rule without a source")
	}
}
