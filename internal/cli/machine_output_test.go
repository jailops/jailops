package cli_test

import (
	"testing"
)

// The machine output of the commands is a contract: scripts parse `-o json` and `-o yaml`. The transcript records the
// main commands in both formats with the data of a small host, so a change of a member name, a type or an order shows
// as a diff of testdata/machine_output.golden. A change that is meant is accepted with `just freeze`.
func TestMachineOutputGoldens(t *testing.T) {
	h := newHarness(t)
	h.seed()
	h.run("", "firewall", "policy", "create", "ssh", "--default-in", "deny")
	h.run("", "firewall", "rule", "add", "ssh", "in", "--action", "allow", "--proto", "tcp", "--port", "22", "--from", "192.0.2.0/24", "--comment", "the office")
	h.run("", "jail", "create", "web", "--image", "freebsd-15", "--network", "lan", "--storage", "default", "--label", "env=prod", "--firewall-policy", "ssh")
	h.run("", "jail", "start", "web")
	tr := h.transcript()
	for _, c := range [][]string{
		{"jail", "list"},
		{"jail", "show", "web"},
		{"jail", "info", "web"},
		{"firewall", "show", "web"},
		{"firewall", "policy", "list"},
		{"firewall", "policy", "show", "ssh"},
		{"network", "list"},
		{"network", "show", "lan"},
		{"project", "list"},
		{"project", "show", "main"},
		{"storage", "list"},
		{"image", "list"},
		{"image", "show", "freebsd-15"},
		{"operation", "list"},
		{"doctor"},
		{"version"},
		{"version", "--client"},
	} {
		for _, f := range []string{"json", "yaml"} {
			tr.do("", append(append([]string{}, c...), "-o", f)...)
		}
	}
	tr.golden("machine_output")
}
