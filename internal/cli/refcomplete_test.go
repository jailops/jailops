package cli_test

import (
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
)

// A reference is completed in two levels, also inside --rule: the kinds where a reference is expected, the names after a
// kind. The candidates are the whole text typed so far with the word completed, so the shell can replace the string.
func TestRuleCompletion(t *testing.T) {
	h := newHarness(t)
	h.seed()
	h.srv.SeedImage(api.Image{Metadata: api.ObjectMeta{Name: "freebsd-151"},
		Spec: api.ImageSpec{OS: api.OSFreeBSD, Release: "15.1-RELEASE", Arch: "amd64", Source: api.ImageSource{Type: "pkgbase"}, Aliases: []string{"stable"}}})
	h.srv.SeedGeneration("freebsd-151")
	for _, args := range [][]string{
		{"app", "install", "postgresql", "--name", "db", "--image", "freebsd-151@stable"},
		{"app", "install", "nginx", "--name", "web", "--image", "freebsd-151@stable"},
		{"firewall", "address-set", "create", "office", "--address", "198.51.100.0/24"},
		{"firewall", "address-set", "create", "vpn", "--address", "203.0.113.0/24"},
	} {
		if o, e, code := h.run("", args...); code != 0 {
			t.Fatalf("%v: %s %s", args, o, e)
		}
	}
	create := []string{"firewall", "policy", "create", "p", "--rule"}
	for _, c := range []struct {
		typed      string
		want, none []string
	}{
		{"", []string{"allow", "deny"}, []string{"in", "from"}},
		{"al", []string{"allow"}, []string{"deny"}},
		{"allow ", []string{"allow in", "allow out"}, nil},
		{"allow in ", []string{"allow in tcp", "allow in udp", "allow in icmp", "allow in icmp6", "allow in from", "allow in port", "allow in comment"}, nil},
		{"allow in tcp ", []string{"allow in tcp from", "allow in tcp to", "allow in tcp port", "allow in tcp disabled"}, []string{"allow in tcp tcp"}},
		{"allow in tcp from ", []string{"allow in tcp from jail:", "allow in tcp from set:", "allow in tcp from tag:", "allow in tcp from any"}, []string{"allow in tcp from host", "allow in tcp from jail:web", "allow in tcp from @web"}},
		{"allow in tcp from j", []string{"allow in tcp from jail:"}, []string{"allow in tcp from set:"}},
		{"allow in tcp from jail:", []string{"allow in tcp from jail:web", "allow in tcp from jail:db"}, []string{"allow in tcp from set:office"}},
		{"allow in tcp from set:", []string{"allow in tcp from set:office", "allow in tcp from set:vpn"}, nil},
		{"allow in tcp from set:office,", []string{"allow in tcp from set:office,jail:", "allow in tcp from set:office,set:", "allow in tcp from set:office,any"}, nil},
		{"allow in tcp from set:office,set:v", []string{"allow in tcp from set:office,set:vpn"}, []string{"allow in tcp from set:office,set:office"}},
		{"allow in tcp from set:office ", []string{"allow in tcp from set:office port", "allow in tcp from set:office comment"}, nil},
		{"allow out tcp to ", []string{"allow out tcp to jail:", "allow out tcp to set:"}, nil},
		{"allow in tcp port ", nil, []string{"allow in tcp port from"}},
		{"allow in tcp comment from ", nil, []string{"allow in tcp comment from jail:"}},
	} {
		out, _, code := h.run("", append(append([]string{"__complete"}, create...), c.typed)...)
		if code != 0 {
			t.Fatalf("%q: %d", c.typed, code)
		}
		has := func(s string) bool {
			for _, l := range strings.Split(out, "\n") {
				if l == s || strings.HasPrefix(l, s+"\t") {
					return true
				}
			}
			return false
		}
		for _, w := range c.want {
			if !has(w) {
				t.Errorf("complete --rule %q: %q missing in %q", c.typed, w, out)
			}
		}
		for _, w := range c.none {
			if has(w) {
				t.Errorf("complete --rule %q: %q offered in %q", c.typed, w, out)
			}
		}
	}
}

// '@' is not part of a reference: a rule, a set member and a source that use it are refused with what to write instead.
func TestAtSignIsRefused(t *testing.T) {
	h := newHarness(t)
	h.seed()
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"firewall", "policy", "create", "p", "--rule", "allow in tcp from @office port 22"}, "@ is not used: write jail:office, set:office or tag:office"},
		{[]string{"firewall", "address-set", "create", "s", "--address", "@web"}, "@ is not used"},
		{[]string{"firewall", "policy", "create", "q", "--rule", "allow in tcp from group:office"}, "unknown kind"},
	} {
		_, e, code := h.run("", c.args...)
		if code == 0 || !strings.Contains(e, c.want) {
			t.Errorf("%v: exit %d, %q does not say %q", c.args, code, e, c.want)
		}
	}
}
