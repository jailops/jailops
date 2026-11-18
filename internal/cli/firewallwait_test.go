package cli

import (
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
)

// firewall wait is done when the condition is true for the rules the policies render now, and for nothing else.
func TestFirewallPending(t *testing.T) {
	ok := api.Condition{Type: api.ConditionFirewallApplied, Status: api.ConditionTrue, Reason: "Applied", Message: "rules aaaaaaaaaaaa are loaded and pf evaluates them"}
	for _, c := range []struct {
		name    string
		conds   []api.Condition
		id      string
		pending bool
		has     string
	}{
		{"confirmed", []api.Condition{{Type: "Ready", Status: api.ConditionTrue}, ok}, "aaaaaaaaaaaa", false, ""},
		{"an older set", []api.Condition{ok}, "bbbbbbbbbbbb", true, "another set"},
		{"false", []api.Condition{{Type: api.ConditionFirewallApplied, Status: api.ConditionFalse, Reason: "StatesPending", Message: "the connections of 1 removed rule(s) are not ended yet"}}, "aaaaaaaaaaaa", true, "StatesPending"},
		{"not set yet", []api.Condition{{Type: "Ready", Status: api.ConditionTrue}}, "aaaaaaaaaaaa", true, "no FirewallApplied condition"},
	} {
		why, pending := firewallPending(c.conds, c.id)
		if pending != c.pending || !strings.Contains(why, c.has) {
			t.Errorf("%s: %q %v", c.name, why, pending)
		}
	}
}
