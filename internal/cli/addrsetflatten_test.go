package cli_test

import (
	"strings"
	"testing"
)

// show --flatten replaces the sets and the jails a set names by their addresses, once each.
func TestAddressSetShowFlatten(t *testing.T) {
	h := newHarness(t)
	h.seed()
	for _, args := range [][]string{
		{"firewall", "address-set", "create", "lupin", "--address", "203.0.113.10", "--address", "2001:db8:a::10"},
		{"firewall", "address-set", "create", "atlas", "--address", "198.51.100.7", "--address", "203.0.113.10"},
		{"firewall", "address-set", "create", "bastion", "--address", "set:lupin", "--address", "set:atlas", "--address", "192.0.2.1"},
	} {
		if o, e, code := h.run("", args...); code != 0 {
			t.Fatalf("%v: %s %s", args, o, e)
		}
	}
	out, errs, code := h.run("", "firewall", "address-set", "show", "bastion", "--flatten")
	if code != 0 {
		t.Fatalf("show --flatten: %s %s", out, errs)
	}
	for _, want := range []string{"flattened: 4 addresses", "192.0.2.1", "198.51.100.7", "203.0.113.10", "2001:db8:a::10"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the output lacks %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "203.0.113.10") != 1 {
		t.Fatalf("an address of two sets is listed once:\n%s", out)
	}
	if strings.Contains(out, "set:") {
		t.Fatalf("no set name is left:\n%s", out)
	}
	// every address says which set lists it; an address of two sets names both
	for _, want := range []string{"ADDRESS", "FROM", "COMMENT", "bastion", "atlas", "lupin"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the table lacks %q:\n%s", want, out)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "203.0.113.10") && !(strings.Contains(line, "lupin") && strings.Contains(line, "atlas")) {
			t.Fatalf("an address of two sets names both: %q", line)
		}
	}
	if o, e, code := h.run("", "firewall", "address-set", "show", "bastion", "--flatten", "-o", "json"); code != 0 || !strings.Contains(o, `"addresses"`) || !strings.Contains(o, `"from"`) {
		t.Fatalf("-o json: %s %s", o, e)
	}
	// without the flag the set is shown as it is: its members, not their addresses
	if o, _, _ := h.run("", "firewall", "address-set", "show", "bastion"); !strings.Contains(o, "set:lupin") {
		t.Fatalf("show without --flatten names the sets:\n%s", o)
	}
}
