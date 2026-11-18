package service_test

import (
	"strings"
	"testing"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/internal/service"
	"jailops.net/jailops/sdk/api"
)

// dualJail makes the jail "web" of the seeded project on a network with an uplink: IPv4 behind NAT, IPv6 routed.
func dualJail(t *testing.T, s *service.Service) {
	t.Helper()
	root := as(globalAdmin, "")
	lan, err := s.GetNetwork(root, "lan")
	if err != nil {
		t.Fatal(err)
	}
	lan.Spec.Uplink = "em0"
	lan.Spec.IPv4.NAT = true
	lan.Spec.IPv6 = &api.IPv6Net{Subnet: "2001:db8:1::/64", Range: "2001:db8:1::10-2001:db8:1::50"}
	if _, err := s.UpdateNetwork(root, "lan", lan, lan.Metadata.Generation); err != nil {
		t.Fatal(err)
	}
	j := dtest.WebJail("web")
	j.Spec.Network[0].IPv6 = "auto"
	op, err := s.CreateJail(as(globalAdmin, dtest.SeedProject), j)
	if err != nil {
		t.Fatal(err)
	}
	if got := wait(t, s, op); got.Status != api.OpSuccess {
		t.Fatalf("create web: %+v", got)
	}
}

func entriesOf(t *testing.T, s *service.Service) []api.ExposeEntry {
	t.Helper()
	j, err := s.GetJail(as(globalAdmin, dtest.SeedProject), "web")
	if err != nil {
		t.Fatal(err)
	}
	return j.Spec.Expose
}

func portsOf(entries []api.ExposeEntry) []string {
	var out []string
	for _, e := range entries {
		out = append(out, e.Protocol()+" "+strings.Join(e.Ports, ",")+" from "+strings.Join(e.From, ","))
	}
	return out
}

// A port is exposed once per protocol: a later exposure takes its ports from the entries that named them, so a
// restriction for one port is not undone by an entry that also names it, and a partial unexpose followed by the full
// request leaves one entry.
func TestAnExposureTakesItsPortsFromTheEntriesThatNamedThem(t *testing.T) {
	_, s := newEnv(t)
	dualJail(t, s)
	admin := as(globalAdmin, dtest.SeedProject)
	if _, err := s.ExposeJail(admin, "web", api.ExposeRequest{Ports: []string{"80,443"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExposeJail(admin, "web", api.ExposeRequest{Ports: []string{"443"}, From: []string{"192.0.2.0/24"}}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(portsOf(entriesOf(t, s)), "; "); got != "tcp 80 from ; tcp 443 from 192.0.2.0/24" {
		t.Fatalf("443 stayed open to anyone: %s", got)
	}
	// the same request again changes nothing
	res, err := s.ExposeJail(admin, "web", api.ExposeRequest{Ports: []string{"443"}, From: []string{"192.0.2.0/24"}})
	if err != nil || res.Changed {
		t.Fatalf("the same exposure again: changed=%v err=%v", res.Changed, err)
	}
	// a part of an entry with the same settings is already what is asked for
	if res, err = s.ExposeJail(admin, "web", api.ExposeRequest{Ports: []string{"80"}}); err != nil || res.Changed {
		t.Fatalf("a port that an entry holds already: changed=%v err=%v", res.Changed, err)
	}
	// unexpose one port, expose the pair again: one entry, no duplicate forwards
	if _, err := s.UnexposeJail(admin, "web", api.UnexposeRequest{Ports: []string{"443"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExposeJail(admin, "web", api.ExposeRequest{Ports: []string{"80,443"}}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(portsOf(entriesOf(t, s)), "; "); got != "tcp 80,443 from " {
		t.Fatalf("the exposure after a partial unexpose and again: %s", got)
	}
}

// Both protocols name each port once in the answer, and the title says what was exposed as the EXPOSED column does.
func TestExposeOfBothProtocolsSaysEachPortOnce(t *testing.T) {
	_, s := newEnv(t)
	dualJail(t, s)
	res, err := s.ExposeJail(as(globalAdmin, dtest.SeedProject), "web", api.ExposeRequest{Ports: []string{"443,80"}, Proto: "tcp,udp"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Exposed != "80,443/tcp,udp" {
		t.Errorf("exposed = %q", res.Exposed)
	}
	for _, f := range res.Families {
		if f.Ports != "80, 443" {
			t.Errorf("family %s names ports %q", f.Family, f.Ports)
		}
		if f.Family == "4" && strings.Join(f.Forwards, ",") != "80->80,443->443" {
			t.Errorf("forwards = %v", f.Forwards)
		}
	}
	if res, err = s.ExposeJail(as(globalAdmin, dtest.SeedProject), "web", api.ExposeRequest{Ports: []string{"53"}, Proto: "udp,tcp,udp"}); err != nil || res.Exposed != "53/tcp,udp" {
		t.Errorf("exposed = %q (%v)", res.Exposed, err)
	}
}

// A host port is for one port: the refusal says what to type.
func TestHostPortOfSeveralPortsSaysWhatToType(t *testing.T) {
	_, s := newEnv(t)
	dualJail(t, s)
	_, err := s.ExposeJail(as(globalAdmin, dtest.SeedProject), "web", api.ExposeRequest{Ports: []string{"8000,8001"}, HostPort: 9000})
	wantCode(t, err, api.CodeInvalid)
	if want := "--host-port takes one port: jailops expose web 8000 --host-port 9000, then jailops expose web 8001"; !strings.Contains(err.Error(), want) {
		t.Errorf("the refusal reads %q, want it to contain %q", err, want)
	}
}

// A policy rule that already lets the port in is named by the exposure, and named again by the unexpose that leaves it open.
func TestExposeAndUnexposeNameThePolicyRuleThatOpensThePort(t *testing.T) {
	_, s := newEnv(t)
	dualJail(t, s)
	admin := as(globalAdmin, dtest.SeedProject)
	if _, err := s.CreatePolicy(admin, api.FirewallPolicy{Metadata: api.ObjectMeta{Name: "web"}, Spec: api.FirewallPolicySpec{DefaultIn: "allow", DefaultOut: "allow",
		Rules: []api.FirewallRule{{Action: "allow", Direction: "in", Proto: "tcp", Ports: []string{"80"}, From: []string{"any"}}}}}); err != nil {
		t.Fatal(err)
	}
	j, _ := s.GetJail(admin, "web")
	j.Spec.Firewall.Policies = []string{"web"}
	if _, err := s.UpdateJail(admin, "web", j, j.Metadata.Generation); err != nil {
		t.Fatal(err)
	}
	res, err := s.ExposeJail(admin, "web", api.ExposeRequest{Ports: []string{"80,443"}})
	if err != nil {
		t.Fatal(err)
	}
	want := "policy web rule 1 already lets 80/tcp in over IPv6; this exposure adds the IPv4 forward"
	if len(res.Notes) != 1 || res.Notes[0] != want {
		t.Fatalf("notes = %q, want %q", res.Notes, want)
	}
	// a port that no policy names has no note
	if res, err = s.ExposeJail(admin, "web", api.ExposeRequest{Ports: []string{"8080"}}); err != nil || len(res.Notes) != 0 {
		t.Fatalf("notes of a port no policy names: %q (%v)", res.Notes, err)
	}
	res, err = s.UnexposeJail(admin, "web", api.UnexposeRequest{Ports: []string{"80"}})
	if err != nil {
		t.Fatal(err)
	}
	if want = "still reachable over IPv6: policy web rule 1"; len(res.Notes) != 1 || res.Notes[0] != want {
		t.Fatalf("notes = %q, want %q", res.Notes, want)
	}
	// the policy rule is untouched
	if p, err := s.GetPolicy(admin, "web"); err != nil || len(p.Spec.Rules) != 1 {
		t.Fatalf("the policy after the unexpose: %+v (%v)", p.Spec, err)
	}
}

// Taking one port out of a range says why nothing happened.
func TestUnexposeOfAPortInsideARangeNamesTheRange(t *testing.T) {
	_, s := newEnv(t)
	dualJail(t, s)
	admin := as(globalAdmin, dtest.SeedProject)
	if _, err := s.ExposeJail(admin, "web", api.ExposeRequest{Ports: []string{"8000-8100"}, Families: []string{"6"}}); err != nil {
		t.Fatal(err)
	}
	res, err := s.UnexposeJail(admin, "web", api.UnexposeRequest{Ports: []string{"8050"}})
	if err != nil || res.Changed {
		t.Fatalf("changed=%v err=%v", res.Changed, err)
	}
	if want := "8050 is inside the exposed range 8000-8100: unexpose the range to remove it"; len(res.Notes) != 1 || res.Notes[0] != want {
		t.Fatalf("notes = %q", res.Notes)
	}
}

// The firewall views name the rules of an exposure "expose": a policy of that name could not be told apart.
func TestAPolicyCannotBeNamedExpose(t *testing.T) {
	_, s := newEnv(t)
	_, err := s.CreatePolicy(as(globalAdmin, dtest.SeedProject), api.FirewallPolicy{Metadata: api.ObjectMeta{Name: "expose"}, Spec: api.FirewallPolicySpec{DefaultIn: "deny", DefaultOut: "allow"}})
	wantCode(t, err, api.CodeInvalid)
	if !strings.Contains(err.Error(), "expose") {
		t.Errorf("the refusal should name it: %v", err)
	}
}
