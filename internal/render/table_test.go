package render

import "testing"

// A table without members is written with its braces: pf replaces the members of an existing table only when the
// definition has them, so a set that lost its last member would otherwise keep admitting its former members.
func TestAnEmptyTableKeepsItsBraces(t *testing.T) {
	if got := tableLine("s_office", nil); got != "table <s_office> persist { }" {
		t.Fatalf("%q", got)
	}
	if got := tableLine("s_office", []string{"192.0.2.7"}); got != "table <s_office> persist { 192.0.2.7 }" {
		t.Fatalf("%q", got)
	}
}
