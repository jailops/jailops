package service

import (
	"reflect"
	"testing"

	"jailops.net/jailops/sdk/api"
)

// A default is deny when any policy of the jail states deny, whatever the order, and allow when none does; the
// policies that state deny are named for the trace. A policy that states nothing, or allow, has no say.
func TestEffectiveDefaults(t *testing.T) {
	rules := api.FirewallPolicySpec{Rules: []api.FirewallRule{{Action: "allow", Direction: "in", Proto: "tcp", Ports: []string{"2282"}, From: []string{"set:office"}}}}
	deny := api.FirewallPolicySpec{DefaultIn: "deny"}
	denyOut := api.FirewallPolicySpec{DefaultOut: "deny"}
	allow := api.FirewallPolicySpec{DefaultIn: "allow", DefaultOut: "allow"}
	for _, tc := range []struct {
		name        string
		pols        []namedSpec
		in, out     string
		inBy, outBy []string
	}{
		{"no policy", nil, "allow", "allow", nil, nil},
		{"a rule-only policy alone leaves the jail open", []namedSpec{{name: "test-access", spec: rules}}, "allow", "allow", nil, nil},
		{"a policy that says allow has no say", []namedSpec{{name: "open", spec: allow}, {name: "test-access", spec: rules}}, "allow", "allow", nil, nil},
		{"one policy denies inbound", []namedSpec{{name: "closed", spec: deny}, {name: "test-access", spec: rules}}, "deny", "allow", []string{"closed"}, nil},
		{"the order does not matter", []namedSpec{{name: "test-access", spec: rules}, {name: "closed", spec: deny}}, "deny", "allow", []string{"closed"}, nil},
		{"the project's deny stays a floor under an allow", []namedSpec{{name: "default", project: true, spec: deny}, {name: "open", spec: allow}}, "deny", "allow", []string{"default [project]"}, nil},
		{"every policy that denies is named", []namedSpec{{name: "default", project: true, spec: deny}, {name: "second", spec: deny}, {name: "own", spec: deny}},
			"deny", "allow", []string{"default [project]", "second", "own"}, nil},
		{"outbound is its own direction", []namedSpec{{name: "egress", spec: denyOut}}, "allow", "deny", nil, []string{"egress"}},
	} {
		in, out, inBy, outBy := effectiveDefaults(tc.pols)
		if in != tc.in || out != tc.out || !reflect.DeepEqual(inBy, tc.inBy) || !reflect.DeepEqual(outBy, tc.outBy) {
			t.Errorf("%s: got in %s %v out %s %v, want in %s %v out %s %v", tc.name, in, inBy, out, outBy, tc.in, tc.inBy, tc.out, tc.outBy)
		}
	}
}
