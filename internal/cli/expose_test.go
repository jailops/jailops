package cli_test

import (
	"testing"

	"jailops.net/jailops/sdk/api"
)

func TestExposeAndUnexpose(t *testing.T) {
	h := newHarness(t)
	h.seed()
	h.srv.SeedJail(api.Jail{Metadata: api.ObjectMeta{Name: "web", Project: "main"}, Spec: api.JailSpec{Image: "freebsd-15@stable", Storage: "default",
		Network: []api.NetworkAttach{{Network: "lan", IPv4: "10.20.0.10", IPv6: "2001:db8::10"}}}})
	tr := h.transcript()
	tr.ok("expose", "web", "80,443", "--comment", "the site")
	tr.ok("expose", "web", "80,443", "--comment", "the site") // again: nothing changes
	tr.ok("expose", "web", "8080", "--from", "192.0.2.0/24", "--host-port", "9090", "--family", "4")
	tr.ok("expose", "web", "53", "--proto", "udp", "--family", "6")
	tr.ok("expose", "--dry-run", "web", "25")
	tr.ok("expose", "web", "22", "-o", "json")
	tr.do("", "expose", "web", "80", "--proto", "sctp")
	tr.do("", "expose", "ghost", "80")
	tr.do("", "expose", "web")
	tr.ok("unexpose", "web", "80")
	tr.ok("unexpose", "web", "--proto", "udp")
	tr.ok("unexpose", "web")
	tr.do("", "unexpose", "ghost")
	tr.golden("expose")
}
