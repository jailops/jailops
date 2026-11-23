package service_test

import (
	"context"
	"strings"
	"testing"

	"jailops.net/jailops/internal/daemon"
	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/internal/sys/run"
	"jailops.net/jailops/sdk/api"
)

// pfAnswers answers `pfctl -s rules` and `pfctl -s memory` with the test's text; everything else goes to the fakes.
type pfAnswers struct {
	run.Runner
	rules, memory *string
}

func (p pfAnswers) Run(ctx context.Context, c run.Cmd) (run.Result, error) {
	if c.Name == "pfctl" {
		switch strings.Join(c.Args, " ") {
		case "-s rules":
			return run.Result{Stdout: []byte(*p.rules)}, nil
		case "-s memory":
			return run.Result{Stdout: []byte(*p.memory)}, nil
		}
	}
	return p.Runner.Run(ctx, c)
}

// doctor warns when the host's pf.conf lets inbound traffic through by default (the anchors never close the uplink),
// and when the jails' state limits add up to more than the host's state table holds, with the numbers.
func TestDoctorHostDefaultDenyAndStates(t *testing.T) {
	rules := `anchor "jailops/*" all` + "\npass out all flags S/SA keep state"
	memory := "states        hard limit   100000\n"
	e := dtest.New(t, dtest.Options{Clients: func(c *daemon.Clients) {
		c.Runner = pfAnswers{c.Runner, &rules, &memory}
	}})
	e.Seed()
	s := e.D.Service()
	ctx := as(globalAdmin, "")
	n, err := s.GetNetwork(ctx, "lan")
	if err != nil {
		t.Fatal(err)
	}
	n.Spec.Uplink = "em0"
	if _, err := s.UpdateNetwork(ctx, "lan", n, n.Metadata.Generation); err != nil {
		t.Fatal(err)
	}
	createJail(t, e, s, "web")
	check := func(name string) api.DoctorCheck {
		t.Helper()
		for _, c := range s.Doctor(ctx).Checks {
			if c.Name == name {
				return c
			}
		}
		return api.DoctorCheck{}
	}
	if c := check("pf.host_default_deny"); c.Status != api.DoctorWarn || !strings.Contains(c.Message, "no inbound default block") || !strings.Contains(c.Hint, "block in") {
		t.Fatalf("%+v", c)
	}
	rules = "block drop all\n" + rules
	if c := check("pf.host_default_deny"); c.Status != api.DoctorOK {
		t.Fatalf("%+v", c)
	}
	if c := check("pf.states"); c.Status != api.DoctorOK || !strings.Contains(c.Message, "100000") {
		t.Fatalf("%+v", c)
	}
	memory = "states        hard limit   5000\n"
	if c := check("pf.states"); c.Status != api.DoctorWarn || !strings.Contains(c.Message, "hard limit 5000") || !strings.Contains(c.Message, "x 10000") || !strings.Contains(c.Hint, "set limit states 20000") {
		t.Fatalf("%+v", c)
	}
	// the running jails together may create more than the table, each well below half of it: information, not a warning
	memory = "states        hard limit   25000\n"
	createJail(t, e, s, "db")
	createJail(t, e, s, "mx")
	if c := check("pf.states"); c.Status != api.DoctorInfo || !strings.Contains(c.Message, "3 running jail(s) may create 30000") {
		t.Fatalf("%+v", c)
	}
	// a stopped jail creates no state
	jailAction(t, s, "mx", "stop")
	if c := check("pf.states"); c.Status != api.DoctorOK || !strings.Contains(c.Message, "2 running jail(s)") {
		t.Fatalf("%+v", c)
	}

	// what pf does not say is not a warning
	memory, rules = "", ""
	if c := check("pf.states"); c.Name != "" {
		t.Fatalf("%+v", c)
	}
	if c := check("pf.host_default_deny"); c.Name != "" {
		t.Fatalf("%+v", c)
	}
}
