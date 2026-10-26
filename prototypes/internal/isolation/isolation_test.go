package isolation

import "testing"

func TestNaming(t *testing.T) {
	for _, j := range Jails {
		if len(j) > 15 || j[:3] != "jop" {
			t.Fatalf("bad jail name %q", j)
		}
		if h, b := EpairHost(j), EpairJail(j); len(h) > 15 || len(b) > 15 || h[len(h)-1] != 'a' || b[len(b)-1] != 'b' {
			t.Fatalf("bad epair names for %q: %q %q", j, h, b)
		}
	}
	if ProjectOf(JailA1) != ProjectA || ProjectOf(JailA2) != ProjectA || ProjectOf(JailB1) != ProjectB {
		t.Fatal("wrong project assignment")
	}
	if p := PeersOf(JailA1); len(p) != 1 || p[0] != JailA2 {
		t.Fatalf("peers of A1 = %v, want [A2]", p)
	}
	if p := PeersOf(JailB1); len(p) != 0 {
		t.Fatalf("peers of B1 = %v, want none", p)
	}
}

func TestAddrSets(t *testing.T) {
	own, other := OwnAddrs(ProjectA), OtherAddrs(ProjectA)
	if len(own) != 4 || len(other) != 2 {
		t.Fatalf("own=%v other=%v", own, other)
	}
	for _, a := range own {
		for _, b := range other {
			if a == b {
				t.Fatalf("own and other overlap: %s", a)
			}
		}
	}
}

func TestValidAnchor(t *testing.T) {
	ok := []string{"jailops/jop8", "jailops/jop8/jop8a", "jailops/jop8/jop8a/jop8a1", "jailops/jop8/jop8b/jop8b1"}
	bad := []string{"jailops/jop3test", "jailops/jop8/../etc", "jailops/jop8x/jop8a", "jailops"}
	for _, a := range ok {
		if !ValidAnchor(a) {
			t.Errorf("expected valid: %q", a)
		}
	}
	for _, a := range bad {
		if ValidAnchor(a) {
			t.Errorf("expected invalid: %q", a)
		}
	}
	if JailAnchor(JailA1) != "jailops/jop8/jop8a/jop8a1" || !ValidAnchor(JailAnchor(JailA1)) {
		t.Fatalf("JailAnchor(A1) = %q", JailAnchor(JailA1))
	}
	if ProjectAnchor(ProjectA) != "jailops/jop8/jop8a" || !ValidAnchor(ProjectAnchor(ProjectA)) {
		t.Fatalf("ProjectAnchor(A) = %q", ProjectAnchor(ProjectA))
	}
}

func TestGuardPfctl(t *testing.T) {
	cases := []struct {
		args []string
		ok   bool
	}{
		{[]string{"-a", "jailops/jop8/jop8a", "-f", "-"}, true},
		{[]string{"-a", "jailops/jop8/jop8a/jop8a1", "-f", "-"}, true},
		{[]string{"-a", "jailops/jop3test", "-f", "-"}, false},
		{[]string{"-a", "jailops/jop8/jop8a", "-f", "/etc/pf.conf"}, false},
		{[]string{"-e"}, false},
		{[]string{"-a", "jailops/jop8/jop8a", "-F", "all"}, true},
		{[]string{"-a", "jailops", "-s", "Anchors"}, true},
		{[]string{"-s", "info"}, true},
		{[]string{"-a", "other/anchor", "-s", "rules"}, false},
	}
	for _, c := range cases {
		err := GuardPfctl(c.args)
		if (err == nil) != c.ok {
			t.Errorf("GuardPfctl(%v) err=%v, want ok=%v", c.args, err, c.ok)
		}
	}
}

func TestRenderJailAnchor(t *testing.T) {
	r := RenderJailAnchor("jop8ea1a", AddrA1V4, AddrA1V6)
	want := "table <jail_addrs> persist { 192.0.2.11, 2001:db8:8::11, fe80::/10, :: }\n" +
		"block in quick on jop8ea1a from ! <jail_addrs> to any\n"
	if r != want {
		t.Fatalf("unexpected: %q", r)
	}
}

func TestRenderProjectAnchor(t *testing.T) {
	r := RenderProjectAnchor(ProjectA, OwnAddrs(ProjectA), OtherAddrs(ProjectA))
	for _, want := range []string{
		"table <jop8a_own> persist", "table <jop8a_other> persist",
		"block return quick from <jop8a_own> to <jop8a_other>",
		"block return quick from <jop8a_other> to <jop8a_own>",
		"pass quick from <jop8a_own> to <jop8a_own>",
	} {
		if !contains(r, want) {
			t.Errorf("missing %q in:\n%s", want, r)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

func TestParseSysctlBool(t *testing.T) {
	if v, ok := ParseSysctlBool(" 1\n"); !v || !ok {
		t.Fatal("want true, ok")
	}
	if v, ok := ParseSysctlBool("0"); v || !ok {
		t.Fatal("want false, ok")
	}
	if _, ok := ParseSysctlBool("garbage"); ok {
		t.Fatal("want not ok")
	}
}

func TestMatrixJSON(t *testing.T) {
	var m Matrix
	m.Add("1", "private=on", JailA1, JailA2, "icmp", true, "ok")
	m.Observe("pfil_member", "1")
	j := m.JSON()
	if j == "" || j[0] != '{' {
		t.Fatalf("bad json: %s", j)
	}
}

func TestValidSysctlName(t *testing.T) {
	for _, n := range []string{SysctlPfilBridge, SysctlPfilMember, SysctlPfilOnlyIP, "net.inet.ip.forwarding", "net.inet6.ip6.forwarding"} {
		if !ValidSysctlName(n) {
			t.Errorf("expected valid: %s", n)
		}
	}
	if ValidSysctlName("kern.securelevel") {
		t.Fatal("must not allow arbitrary sysctl")
	}
}
