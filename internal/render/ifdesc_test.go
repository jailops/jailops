package render

import (
	"jailops.net/jailops/internal/sys/ifconfig"
	"net/netip"
	"strings"
	"testing"
)

func TestIfaceDescriptions(t *testing.T) {
	long := strings.Repeat("a", 63)
	var b strings.Builder
	for _, c := range []struct{ what, got string }{
		{"jail host side", JailIfaceDesc("web", "nginx", "lan", true)},
		{"jail jail side", JailIfaceDesc("web", "nginx", "lan", false)},
		{"wireguard", WireGuardDesc("office")},
		{"long jail", JailIfaceDesc(long[:31], long[:31], long, true)},
		{"long jail side", JailIfaceDesc("p", long[:61], "net-1", false)},
		{"long wireguard", WireGuardDesc(long)},
		{"odd characters", JailIfaceDesc("w b", "x\"y", "l\n", true)},
	} {
		if len(c.got) > IfaceDescMax || !ValidIfaceDesc(c.got) || !IsJailOpsDesc(c.got) {
			t.Errorf("%s: %q is not a valid description", c.what, c.got)
		}
		b.WriteString(c.what + ": " + c.got + "\n")
	}
	golden(t, "iface_descriptions", b.String())
	// deterministic: the same names always give the same text
	if JailIfaceDesc(long, long, long, true) != JailIfaceDesc(long, long, long, true) {
		t.Fatal("not deterministic")
	}
}

func TestIfaceDescOwnership(t *testing.T) {
	want := JailIfaceDesc("web", "nginx", "lan", true)
	for _, c := range []struct {
		desc string
		own  bool
	}{
		{want, true},
		{ifconfig.Marker, true},      // the bare marker of an interface just made
		{"jailops:n-1", false},       // another form is not JailOps's
		{"", false},                  // may be anybody's
		{"uplink to the lab", false}, // the administrator's own text
		{JailIfaceDesc("web", "nginx", "dmz", true), false},
	} {
		if got := OwnsIface(c.desc, want); got != c.own {
			t.Errorf("OwnsIface(%q) = %v", c.desc, got)
		}
	}
	for _, d := range []string{"jailops jail=web/nginx net=lan (host side)", ""} {
		if !ValidIfaceDesc(d) {
			t.Errorf("%q refused", d)
		}
	}
	for _, d := range []string{strings.Repeat("x", 65), "a\nb", "a\"b", "a;b", "a$b", "a`b", "ä", "a\tb", "a#b", "a,b", "a'b"} {
		if ValidIfaceDesc(d) {
			t.Errorf("%q accepted", d)
		}
	}
}

func TestPFWireGuard(t *testing.T) {
	tun := []netip.Prefix{netip.MustParsePrefix("10.99.0.0/24"), netip.MustParsePrefix("fd99::/64")}
	host := []netip.Addr{netip.MustParseAddr("203.0.113.2"), netip.MustParseAddr("10.20.0.1"), netip.MustParseAddr("10.99.0.1")}
	served := []netip.Prefix{netip.MustParsePrefix("10.20.0.0/24"), netip.MustParsePrefix("2001:db8:1:2:100::/80")}
	rs, err := PFWireGuard("office", "wg-office", tun, host, served)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "pf_wireguard", rs)
	// the anchor holds the protection of the host and no pass at all: what a peer reaches in a jail is decided by the
	// jail's own allow in rules
	for _, ln := range strings.Split(rs, "\n") {
		if strings.HasPrefix(ln, "pass ") {
			t.Errorf("a pass rule in the anchor of a tunnel: %q", ln)
		}
	}
	for _, want := range []string{"block drop in quick on wg-office from any to <host>", "block drop in quick on wg-office from ! <tunnel_nets> to any", "block drop in quick on wg-office from any to ! <served_nets>"} {
		if !strings.Contains(rs, want) {
			t.Errorf("missing %q in:\n%s", want, rs)
		}
	}
	// a tunnel that serves no network lets a peer reach nothing
	none, err := PFWireGuard("office", "wg-office", tun, host, nil)
	if err != nil || !strings.Contains(none, "block drop in quick on wg-office all") || strings.Contains(none, "served_nets") {
		t.Errorf("a tunnel with no served network: %v\n%s", err, none)
	}
	for _, bad := range [][2]string{{"Office", "wg-office"}, {"office", "em0"}, {"office", "wg office"}, {"office", "wg-0123456789abc"}} {
		if _, err := PFWireGuard(bad[0], bad[1], tun, host, served); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
