package server_test

import (
	"strings"
	"testing"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/internal/render"
)

// The egress limit of a device is a dummynet pipe: the jail's start configures it, and its anchor sends what the jail
// sends into it, on the jail's own port and on the gateway interface (the pipe of the direction into the jail is 0).
func TestEgressLimitIsEnforcedWithAPipe(t *testing.T) {
	e := seeded(t)
	c := ctxT(t)
	j := dtest.WebJail("web")
	j.Spec.Network[0].Egress = "5M"
	startJailIn(t, e.Admin, j)
	var cfg []string
	for _, a := range e.Run.Argv() {
		if strings.HasPrefix(a, "dnctl pipe ") && strings.Contains(a, " config bw ") {
			cfg = append(cfg, a)
		}
	}
	if len(cfg) != 1 || !strings.HasSuffix(cfg[0], "bw 5000000bit/s") {
		t.Fatalf("the pipe is not 5 Mbit/s: %q", cfg)
	}
	e.D.Reconciler().Pass(c)
	anchor, err := e.PF.Read(c, render.JailAnchorFor("alpha", "web"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(anchor, "dnpipe") != 2 || !strings.Contains(anchor, "match in on ") || !strings.Contains(anchor, "dnpipe (0, ") {
		t.Fatalf("the anchor does not send what the jail sends into the pipe:\n%s", anchor)
	}
	// a jail without a limit has no pipe and no dnpipe
	startJailIn(t, e.Admin, dtest.WebJail("plain"))
	e.D.Reconciler().Pass(c)
	plain, _ := e.PF.Read(c, render.JailAnchorFor("alpha", "plain"))
	if strings.Contains(plain, "dnpipe") {
		t.Fatalf("an unlimited jail has dnpipe:\n%s", plain)
	}
	// ingress is not a key: traffic into the jail cannot be limited
	if _, err := e.Admin.Jails().Config(c, "web", map[string]string{"nic.eth0.ingress": "10M"}, nil, false); err == nil {
		t.Fatal("an ingress limit was accepted")
	}
	// a rate that is not a rate is refused
	if _, err := e.Admin.Jails().Config(c, "web", map[string]string{"nic.eth0.egress": "fast"}, nil, false); err == nil {
		t.Fatal("a rate that is not a rate was accepted")
	}
}
