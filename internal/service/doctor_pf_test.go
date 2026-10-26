package service

import (
	"strings"
	"testing"
)

func TestQuickPassesBeforeTheAnchorAreFound(t *testing.T) {
	rules := strings.Join([]string{
		"block drop log all",
		"pass in quick on vtnet0 proto tcp from any to (vtnet0) port = 22 flags S/SA keep state",
		"pass out quick on vtnet0 all flags S/SA keep state",
		"pass quick inet proto icmp all keep state",
		"pass quick inet6 proto ipv6-icmp all keep state",
		`anchor "jailops/*" all`,
		"pass quick all",
	}, "\n")
	got := quickPassesBeforeAnchor(rules)
	if len(got) != 2 || !strings.Contains(got[0], "icmp") || !strings.Contains(got[1], "ipv6-icmp") {
		t.Fatalf("%q", got)
	}
	if got := quickPassesBeforeAnchor("pass all\n" + `anchor "jailops/*" all`); len(got) != 0 {
		t.Fatalf("a rule that is not quick was reported: %q", got)
	}
}
