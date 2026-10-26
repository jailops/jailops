package pfanchor

import "testing"

func TestValidAnchor(t *testing.T) {
	for s, want := range map[string]bool{"jailops/jop3a": true, "jailops/jop3_shared": true, "jailops/web": false, "jailops": false, "": false,
		"jailops/jop3a/b": false, "jailops/jop3a;rm": false, "other/jop3a": false} {
		if ValidAnchor(s) != want {
			t.Errorf("%q", s)
		}
	}
}

func TestGuard(t *testing.T) {
	ok := [][]string{
		{"-s", "info"}, {"-sr"}, {"-a", "jailops/jop3a", "-sr"}, {"-a", "jailops/jop3a", "-f", "-"},
		{"-n", "-a", "jailops/jop3a", "-f", "-"}, {"-a", "jailops/jop3a", "-F", "all"},
		{"-a", "jailops/jop3a", "-t", "x", "-T", "add", "192.0.2.1"}, {"-a", "jailops", "-sA"},
	}
	for _, a := range ok {
		if err := GuardPfctl(a); err != nil {
			t.Errorf("%v: %v", a, err)
		}
	}
	bad := [][]string{
		{"-f", "/etc/pf.conf"}, {"-F", "all"}, {"-a", "jailops/web", "-F", "all"}, {"-e"}, {"-d"},
		{"-a", "jailops/jop3a", "-f", "/etc/pf.conf"}, {"-T", "flush", "-t", "x"}, {"-a", "other", "-sr"},
		{"-k", "1.2.3.4"},
	}
	for _, a := range bad {
		if GuardPfctl(a) == nil {
			t.Errorf("%v accepted", a)
		}
	}
}

func TestClassify(t *testing.T) {
	v1, v2 := Render("v1"), Render("v2")
	if Classify(v1) != StateV1 || Classify(v2) != StateV2 || Classify(" \n") != StateEmpty ||
		Classify(v1+v2) != StateMixed || Classify("pass all") != StateOther {
		t.Fatal("classify")
	}
}

func TestAnchorLines(t *testing.T) {
	conf := "nat-anchor \"jailops/*\"\n# anchor \"jailops/*\"\nrdr-anchor \"jailops/*\"\n"
	a := ParseAnchorLines(conf)
	if !a.NatAnchor || !a.RdrAnchor || a.Anchor || a.All() {
		t.Fatalf("%+v", a)
	}
	if !ParseAnchorLines(conf + "  anchor \"jailops/*\" in\n").All() {
		t.Fatal("all")
	}
}

func TestStatus(t *testing.T) {
	e, k := PfStatusEnabled("Status: Enabled for 0 days 00:01:02           Debug: Urgent\n")
	if !e || !k {
		t.Fatal()
	}
	e, k = PfStatusEnabled("Status: Disabled")
	if e || !k {
		t.Fatal()
	}
	if _, k = PfStatusEnabled("x"); k {
		t.Fatal()
	}
}
