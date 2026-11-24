package service_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"jailops.net/jailops/internal/daemon"
	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/internal/sys/run"
	"jailops.net/jailops/sdk/api"
)

// hostReads answers the read-only questions the doctor checks of the host's pf.conf and devfs rulesets ask
// (pfctl -s rules|nat, devfs rule showsets|-s N show) with the test's text; everything else goes to the fakes.
type hostReads struct {
	run.Runner
	answers map[string]string
}

func (h hostReads) Run(ctx context.Context, c run.Cmd) (run.Result, error) {
	if out, ok := h.answers[c.Name+" "+strings.Join(c.Args, " ")]; ok {
		return run.Result{Stdout: []byte(out)}, nil
	}
	return h.Runner.Run(ctx, c)
}

func hostReadsEnv(t *testing.T, answers map[string]string, netstat *string) (*dtest.Env, func(string) api.DoctorCheck) {
	t.Helper()
	e := dtest.New(t, dtest.Options{Clients: func(c *daemon.Clients) {
		c.Runner = hostReads{c.Runner, answers}
		c.HostExec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name == "netstat" && strings.Join(args, " ") == "-rnW" {
				return []byte(*netstat), nil
			}
			return nil, context.Canceled
		}
	}})
	e.Seed()
	s := e.D.Service()
	return e, func(name string) api.DoctorCheck {
		t.Helper()
		for _, c := range s.Doctor(as(globalAdmin, "")).Checks {
			if c.Name == name {
				return c
			}
		}
		return api.DoctorCheck{}
	}
}

// A legacy rdr in pf.conf is evaluated before the forwards of jailops expose and wins: doctor names the rule and the
// exposures it takes, and stays quiet about one on another port or interface.
func TestDoctorLegacyRdr(t *testing.T) {
	answers := map[string]string{"pfctl -s rules": `anchor "jailops/*" all` + "\nblock drop in all\n"}
	netstat := ""
	e, check := hostReadsEnv(t, answers, &netstat)
	s := e.D.Service()
	natLan(t, s)
	seedJail(t, s, "web")
	if _, err := s.ExposeJail(as(globalAdmin, dtest.SeedProject), "web", api.ExposeRequest{Ports: []string{"8080"}}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		nat    string
		status api.DoctorStatus
		want   string
	}{
		{`rdr-anchor "jailops/*" all` + "\nnat on em0 inet from 10.0.0.0/24 to any -> (em0)", api.DoctorOK, "no rdr"},
		{"rdr pass on em0 inet proto tcp from any to any port = 2222 -> 127.0.0.1 port 22", api.DoctorOK, "no rdr"},
		{"rdr pass on em1 inet proto tcp from any to any port = 8080 -> 10.9.0.2", api.DoctorOK, "no rdr"},
		{"rdr on em0 inet proto udp from any to any port = 8080 -> 10.9.0.2", api.DoctorOK, "no rdr"},
		{"no rdr on em0 inet proto tcp from any to any port = 8080", api.DoctorOK, "no rdr"},
		{"rdr pass on em0 inet proto tcp from any to any port = 8080 -> 10.9.0.2 port 80", api.DoctorWarn, dtest.SeedProject + "/web tcp/8080 on em0"},
		{"rdr on em0 inet proto tcp from any to any port 8000:8100 -> 10.9.0.2", api.DoctorWarn, "tcp/8080"},
		{"rdr on egress inet proto tcp from any to any -> 10.9.0.2", api.DoctorWarn, "tcp/8080"},
		{"binat on em0 from 10.9.0.2 to any -> 192.0.2.7", api.DoctorWarn, "binat"},
	} {
		answers["pfctl -s nat"] = tc.nat
		c := check("pf.legacy_rdr")
		if c.Status != tc.status || !strings.Contains(c.Message, tc.want) {
			t.Errorf("%q: %s %s", tc.nat, c.Status, c.Message)
		}
	}
}

// uRPF on an uplink of the jails drops a client whose route back leaves through another interface: doctor warns when a
// default route leaves elsewhere, and says nothing when the uplink carries every default route.
func TestDoctorURPF(t *testing.T) {
	answers := map[string]string{"pfctl -s nat": ""}
	netstat := "Routing tables\n\nInternet:\nDestination        Gateway            Flags   Nhop#    Mtu      Netif Expire\ndefault            192.0.2.1          UGS         1   1500        em0\n"
	e, check := hostReadsEnv(t, answers, &netstat)
	s := e.D.Service()
	natLan(t, s)
	seedJail(t, s, "web")
	answers["pfctl -s rules"] = `anchor "jailops/*" all` + "\nblock drop in all\n"
	if c := check("pf.urpf"); c.Name != "" {
		t.Fatalf("no urpf-failed rule, no check: %+v", c)
	}
	answers["pfctl -s rules"] = `anchor "jailops/*" all` + "\nblock drop in quick from urpf-failed to any label \"uRPF\"\n"
	if c := check("pf.urpf"); c.Status != api.DoctorOK {
		t.Fatalf("one uplink with the default route: %+v", c)
	}
	netstat += "\nInternet6:\nDestination        Gateway            Flags   Nhop#    Mtu      Netif Expire\ndefault            fe80::1%em1        UGS         2   1500        em1\n"
	if c := check("pf.urpf"); c.Status != api.DoctorWarn || !strings.Contains(c.Message, "em0 (a default route leaves through em1)") || !strings.Contains(c.Hint, "on IF") {
		t.Fatalf("a second default route elsewhere: %+v", c)
	}
	answers["pfctl -s rules"] = `anchor "jailops/*" all` + "\nblock drop in quick on em1 from urpf-failed to any\n"
	if c := check("pf.urpf"); c.Name != "" {
		t.Fatalf("uRPF on another interface only: %+v", c)
	}
}

// The stock devfs rulesets the jails build on are compared with FreeBSD's: an extra unhide in ruleset 4 is named with
// the jails without devices that get it, one in 1 to 3 with every FreeBSD jail, and a ruleset 4 that is not loaded fails.
func TestDoctorDevfsRuleset(t *testing.T) {
	login := ""
	for _, p := range []string{"pty", "tty"} {
		for _, c := range "pqrsPQRSlmnoLMNO" {
			login += "path " + p + string(c) + "* unhide\n"
		}
	}
	login += "path ptmx unhide\npath pts unhide\npath pts/* unhide\npath fd unhide\npath fd/* unhide\npath stdin unhide\npath stdout unhide\npath stderr unhide\n"
	number := func(rules string) string { // as devfs rule -s N show prints them: 100, 200, ...
		var out []string
		for i, r := range strings.Split(strings.TrimSpace(rules), "\n") {
			out = append(out, fmt.Sprintf("%d %s", (i+1)*100, r))
		}
		return strings.Join(out, "\n") + "\n"
	}
	answers := map[string]string{
		"devfs rule showsets":  "1\n2\n3\n4\n5\n",
		"devfs rule -s 1 show": number("hide"),
		"devfs rule -s 2 show": number("path null unhide\npath zero unhide\npath crypto unhide\npath random unhide\npath urandom unhide"),
		"devfs rule -s 3 show": number(login),
		"devfs rule -s 4 show": number("include 1\ninclude 2\ninclude 3\npath fuse unhide\npath zfs unhide"),
	}
	netstat := ""
	e, check := hostReadsEnv(t, answers, &netstat)
	s := e.D.Service()
	seedJail(t, s, "web")
	seedJail(t, s, "vpn")
	op, err := s.ConfigureJail(as(globalAdmin, dtest.SeedProject), "vpn", api.ConfigChange{Set: map[string]string{"devfs.t.device": "tun"}}, false)
	mustOp(t, s, op, err)
	if c := check("jail.devfs_ruleset"); c.Status != api.DoctorOK {
		t.Fatalf("stock rulesets: %+v", c)
	}
	// the CI script's pollution: bpf in ruleset 4, many times over; the jail with devices has its own ruleset
	answers["devfs rule -s 4 show"] = number("include 1\ninclude 2\ninclude 3\npath fuse unhide\npath zfs unhide\npath bpf* unhide\npath bpf* unhide\npath 'pf' unhide")
	c := check("jail.devfs_ruleset")
	if c.Status != api.DoctorWarn || !strings.Contains(c.Message, "ruleset 4 has path bpf* unhide, path pf unhide, which the jail(s) "+dtest.SeedProject+"/web get") || strings.Contains(c.Message, "vpn") || !strings.Contains(c.Hint, "devfs.NAME.device") {
		t.Fatalf("bpf in ruleset 4: %+v", c)
	}
	// ruleset 1 that hides nothing reaches every FreeBSD jail
	answers["devfs rule -s 4 show"] = number("include 1\ninclude 2\ninclude 3\npath fuse unhide\npath zfs unhide")
	answers["devfs rule -s 1 show"] = number("path dri unhide")
	c = check("jail.devfs_ruleset")
	if c.Status != api.DoctorWarn || !strings.Contains(c.Message, "nothing is hidden") || !strings.Contains(c.Message, "vpn") {
		t.Fatalf("ruleset 1: %+v", c)
	}
	// ruleset 4 not loaded: a jail on it sees the whole /dev
	answers["devfs rule showsets"] = "1\n2\n3\n"
	if c := check("jail.devfs_ruleset"); c.Status != api.DoctorFail || !strings.Contains(c.Message, "whole /dev") {
		t.Fatalf("no ruleset 4: %+v", c)
	}
}
