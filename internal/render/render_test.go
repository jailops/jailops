package render

import (
	"net/netip"
	"regexp"
	"strings"
	"testing"
	"time"

	"jailops.net/jailops/sdk/api"
)

func ip(s string) netip.Addr    { return netip.MustParseAddr(s) }
func pfx(s string) netip.Prefix { return netip.MustParsePrefix(s) }
func intp(i int) *int           { return &i }

func baseConf() JailConfInput {
	return JailConfInput{
		Name: "alpha_web", Path: "/jailops/projects/alpha/jails/web/root", Hostname: "web.example.org", OS: api.OSFreeBSD,
		Interfaces: []string{"jo0123456789a0b"}, EnforceStatfs: intp(2), SecureLevel: intp(2),
		OSRelease: "15.0-RELEASE", Allow: []string{"sysvipc", "raw_sockets", "mount.zfs"},
	}
}

func TestJailConfGolden(t *testing.T) {
	s, err := JailConf(baseConf())
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "jailconf_freebsd", s)
	l := baseConf()
	l.Name, l.OS, l.Allow, l.OSRelease, l.SecureLevel = "alpha_alpine1", api.OSLinux, []string{"socket_af"}, "", nil
	l.FstabPath = "/jailops/projects/alpha/jails/alpine1/fstab"
	s, err = JailConf(l)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "jailconf_linux", s)
}

func TestJailConfExecFixed(t *testing.T) {
	// exec.* lines never contain input-derived text, whatever the input.
	a, _ := JailConf(baseConf())
	l := baseConf()
	l.Name, l.Path, l.Hostname, l.Interfaces = "alpha_other", "/x/y", "h.example", []string{"em0"}
	b, _ := JailConf(l)
	exec := func(s string) string {
		var out []string
		for _, ln := range strings.Split(s, "\n") {
			if strings.HasPrefix(strings.TrimSpace(ln), "exec.") {
				out = append(out, ln)
			}
		}
		return strings.Join(out, "\n")
	}
	if exec(a) != exec(b) || !strings.Contains(exec(a), "${name}") {
		t.Fatalf("exec lines depend on input:\n%s\n%s", exec(a), exec(b))
	}
}

func TestJailConfErrors(t *testing.T) {
	tests := []struct {
		name string
		mut  func(*JailConfInput)
	}{
		{"name newline", func(i *JailConfInput) { i.Name = "a\n}\nb {" }},
		{"name upper", func(i *JailConfInput) { i.Name = "Web" }},
		{"path quote", func(i *JailConfInput) { i.Path = "/a\"; exec.start=\"x" }},
		{"path var", func(i *JailConfInput) { i.Path = "/a/$name" }},
		{"path relative", func(i *JailConfInput) { i.Path = "a/b" }},
		{"hostname", func(i *JailConfInput) { i.Hostname = "a b;" }},
		{"os", func(i *JailConfInput) { i.OS = "plan9" }},
		{"iface", func(i *JailConfInput) { i.Interfaces = []string{"em0\";"} }},
		{"allow", func(i *JailConfInput) { i.Allow = []string{"mount.fusefs"} }},
		{"allow inject", func(i *JailConfInput) { i.Allow = []string{"x; exec.start=\"y\""} }},
		{"allow dup", func(i *JailConfInput) { i.Allow = []string{"sysvipc", "sysvipc"} }},
		{"statfs", func(i *JailConfInput) { i.EnforceStatfs = intp(3) }},
		{"securelevel", func(i *JailConfInput) { i.SecureLevel = intp(9) }},
		{"devfs", func(i *JailConfInput) { i.DevfsRuleset = 70000 }},
		{"osrelease", func(i *JailConfInput) { i.OSRelease = "15.0\";" }},
		{"fstab", func(i *JailConfInput) { i.FstabPath = "/a b" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := baseConf()
			tc.mut(&in)
			if s, err := JailConf(in); err == nil {
				t.Fatalf("accepted:\n%s", s)
			}
		})
	}
}

var jailLineRE = regexp.MustCompile(`^(# Managed by JailOps\. Do not edit; changes are overwritten\.|[a-z][a-z0-9-]* \{|\}|` +
	`\t(path|host\.hostname|osrelease|mount\.fstab) = "[A-Za-z0-9._+@:=,/%-]+";|` +
	`\tvnet\.interface = "[a-z0-9]+"(, "[a-z0-9]+")*;|` +
	`\t(vnet|mount\.devfs|exec\.clean|persist|allow\.[a-z_.]+);|` +
	`\t(devfs_ruleset|enforce_statfs|securelevel) = -?[0-9]+;|` +
	`\texec\.(prestart|poststart|prestop|poststop) = "/usr/local/libexec/jailops-priv hook (prestart|poststart|prestop|poststop) \$\{name\}";|` +
	`\texec\.(start = "/bin/sh /etc/rc"|stop = "/bin/sh /etc/rc\.shutdown jail");)$`)

func FuzzJailConf(f *testing.F) {
	f.Add("web", "/a/b", "h.example", "em0", "sysvipc", "15.0-RELEASE")
	f.Add("a\n}\nweb {", "/a\"\nexec.start=\"x", "h\n", "e;", "x\nallow.mount", "15\n")
	f.Fuzz(func(t *testing.T, name, path, host, ifn, allow, rel string) {
		in := JailConfInput{Name: name, Path: path, Hostname: host, OS: api.OSFreeBSD, Interfaces: []string{ifn}, Allow: []string{allow}, OSRelease: rel, FstabPath: path}
		for _, os := range []api.OS{api.OSFreeBSD, api.OSLinux} {
			in.OS = os
			out, err := JailConf(in)
			if err != nil {
				continue
			}
			lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
			opens := 0
			for _, ln := range lines {
				if !jailLineRE.MatchString(ln) {
					t.Fatalf("unexpected line %q in\n%s", ln, out)
				}
				if strings.HasSuffix(ln, " {") {
					opens++
				}
			}
			if opens != 1 || lines[len(lines)-1] != "}" {
				t.Fatalf("block structure broken:\n%s", out)
			}
		}
	})
}

func TestFstabGolden(t *testing.T) {
	s, err := Fstab(FstabInput{OS: api.OSLinux, Root: "/jailops/jails/alpine1/root", ShmSize: "512M", DevfsRuleset: 7123,
		Volumes: []FstabVolume{{Source: "/jailops/jails/alpine1/data/db", Target: "/var/lib/db"}, {Source: "/jailops/jails/alpine1/data/ro", Target: "/srv/ro", ReadOnly: true}}})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "fstab_linux", s)
	s, err = Fstab(FstabInput{OS: api.OSFreeBSD, Root: "/jailops/jails/web/root", Volumes: []FstabVolume{{Source: "/jailops/jails/web/data/www", Target: "/usr/local/www"}}})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "fstab_freebsd", s)
	for _, bad := range []FstabInput{
		{OS: api.OSFreeBSD, Root: "/"},
		{OS: api.OSFreeBSD, Root: "/a b"},
		{OS: "x", Root: "/a"},
		{OS: api.OSLinux, Root: "/a", ShmSize: "lots"},
		{OS: api.OSFreeBSD, Root: "/a", Volumes: []FstabVolume{{Source: "/x\n/etc nullfs rw 0 0", Target: "/y"}}},
		{OS: api.OSFreeBSD, Root: "/a", Volumes: []FstabVolume{{Source: "/x", Target: "/y z"}}},
	} {
		if _, err := Fstab(bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func FuzzFstab(f *testing.F) {
	f.Add("/r", "/s", "/t")
	f.Add("/r\n", "/s z", "/t\n/etc")
	f.Fuzz(func(t *testing.T, root, src, tgt string) {
		for _, os := range []api.OS{api.OSFreeBSD, api.OSLinux} {
			out, err := Fstab(FstabInput{OS: os, Root: root, Volumes: []FstabVolume{{Source: src, Target: tgt}}})
			if err != nil {
				continue
			}
			for _, ln := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
				if strings.HasPrefix(ln, "#") {
					continue
				}
				if len(strings.Fields(ln)) != 6 || strings.Count(ln, " ") != 5 {
					t.Fatalf("bad fstab line %q", ln)
				}
			}
		}
	})
}

func guestNet() NetInput {
	return NetInput{
		Hostname: "web.example.org",
		Ifaces: []NetIface{
			{Name: "jo0123456789a0b", IPv4: pfx("10.20.0.10/24"), IPv6: pfx("2001:db8:1::10/64"), Gateway4: ip("10.20.0.1"), Gateway6: ip("fe80::1"), MTU: 1500},
			{Name: "jo0123456789a1b", IPv4: pfx("10.30.0.10/16")},
		},
		DNS:    []netip.Addr{ip("9.9.9.9"), ip("2620:fe::fe")},
		Search: []string{"example.org"},
	}
}

func TestGuestGolden(t *testing.T) {
	s, err := RcConfFreeBSD(guestNet())
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "rcconf_freebsd", s)
	v4only := NetInput{Hostname: "db", Ifaces: []NetIface{{Name: "epair0b", IPv4: pfx("192.168.1.5/25"), Gateway4: ip("192.168.1.1")}}}
	if s, err = RcConfFreeBSD(v4only); err != nil {
		t.Fatal(err)
	}
	golden(t, "rcconf_freebsd_v4only", s)
	proxy := NetInput{Hostname: "p", Ifaces: []NetIface{{Name: "jo0123456789a0b", IPv6: pfx("2001:db8:2::5/128"), Gateway6: ip("fe80::1")}}}
	if s, err = RcConfFreeBSD(proxy); err != nil {
		t.Fatal(err)
	}
	golden(t, "rcconf_freebsd_proxy", s)
	if s, err = AlpineInterfaces(guestNet()); err != nil {
		t.Fatal(err)
	}
	golden(t, "alpine_interfaces", s)
	if s, err = ResolvConf(guestNet().DNS, guestNet().Search); err != nil {
		t.Fatal(err)
	}
	golden(t, "resolv_conf", s)
	if s, err = AlpineResolvConf(nil, nil); err != nil {
		t.Fatal(err)
	}
	golden(t, "resolv_conf_empty", s)
	if h, err := AlpineHostname("web"); err != nil || h != "web\n" {
		t.Fatal(h, err)
	}
}

func TestGuestErrors(t *testing.T) {
	bad := []NetInput{
		{Hostname: "a b"},
		{Hostname: "a\nb"},
		{Hostname: "a", Ifaces: []NetIface{{Name: "em0\""}}},
		{Hostname: "a", Ifaces: []NetIface{{Name: "em0"}, {Name: "em0"}}},
		{Hostname: "a", Ifaces: []NetIface{{Name: "em0", IPv4: pfx("2001:db8::1/64")}}},
		{Hostname: "a", Ifaces: []NetIface{{Name: "em0", IPv6: pfx("10.0.0.1/8")}}},
		{Hostname: "a", Ifaces: []NetIface{{Name: "em0", Gateway4: ip("10.0.0.1")}}},
		{Hostname: "a", Ifaces: []NetIface{{Name: "em0", IPv6: pfx("2001:db8::1/64"), Gateway6: ip("fe80::1%em0")}}},
		{Hostname: "a", Ifaces: []NetIface{{Name: "em0", MTU: 5}}},
	}
	for i, in := range bad {
		if _, err := RcConfFreeBSD(in); err == nil {
			t.Errorf("rcconf %d accepted", i)
		}
		if _, err := AlpineInterfaces(in); err == nil {
			t.Errorf("alpine %d accepted", i)
		}
	}
	if _, err := ResolvConf([]netip.Addr{ip("1.1.1.1"), ip("1.0.0.1"), ip("8.8.8.8"), ip("8.8.4.4")}, nil); err == nil {
		t.Error("4 nameservers accepted")
	}
	if _, err := ResolvConf(nil, []string{"a b"}); err == nil {
		t.Error("bad search")
	}
	if _, err := ResolvConf([]netip.Addr{ip("0.0.0.0")}, nil); err == nil {
		t.Error("unspecified nameserver")
	}
	if _, err := ResolvConf([]netip.Addr{{}}, nil); err == nil {
		t.Error("invalid nameserver")
	}
}

var (
	rcLineRE    = regexp.MustCompile(`^(#.*|[a-z0-9_]+="[A-Za-z0-9 .:/%_-]*")$`)
	ifaceLineRE = regexp.MustCompile(`^(# Managed.*|auto [a-z0-9]+|auto lo|iface lo inet loopback|iface [a-z0-9]+ inet6? (static|manual)|\t(address|gateway) [0-9a-f:./]+|\tmtu [0-9]+|)$`)
	resolvRE    = regexp.MustCompile(`^(# Managed.*|search [A-Za-z0-9. -]+|nameserver [0-9a-f:.]+)$`)
)

func FuzzGuest(f *testing.F) {
	f.Add("web", "em0", "10.0.0.5", "example.org")
	f.Add("a\"\nrm", "em0\n", "10.0.0.5\n", "x\ny")
	f.Fuzz(func(t *testing.T, host, ifn, addr, search string) {
		in := NetInput{Hostname: host, Ifaces: []NetIface{{Name: ifn}}, Search: []string{search}}
		if a, err := netip.ParseAddr(addr); err == nil {
			in.DNS = []netip.Addr{a}
			if a.Is4() {
				in.Ifaces[0].IPv4 = netip.PrefixFrom(a, 24)
				in.Ifaces[0].Gateway4 = a
			} else {
				in.Ifaces[0].IPv6 = netip.PrefixFrom(a, 64)
				in.Ifaces[0].Gateway6 = a
			}
		}
		check := func(name string, re *regexp.Regexp, out string, err error) {
			if err != nil {
				return
			}
			for _, ln := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
				if !re.MatchString(ln) {
					t.Fatalf("%s: unexpected line %q in\n%s", name, ln, out)
				}
			}
		}
		o, err := RcConfFreeBSD(in)
		check("rc", rcLineRE, o, err)
		o, err = AlpineInterfaces(in)
		check("ifaces", ifaceLineRE, o, err)
		o, err = ResolvConf(in.DNS, in.Search)
		check("resolv", resolvRE, o, err)
	})
}

func TestPlansGolden(t *testing.T) {
	n := api.NetworkSpec{
		Uplink: "em0", MTU: 1500,
		IPv4: &api.IPv4Net{Subnet: "10.20.0.0/24", Gateway: "10.20.0.1", Range: "10.20.0.10-10.20.0.200", NAT: true},
		IPv6: &api.IPv6Net{Mode: api.IPv6Routed, Subnet: "2001:db8:1::/64", Gateway: "2001:db8:1::1", Range: "2001:db8:1::100-2001:db8:1::ffff"},
	}
	steps, err := NetworkPlan(NetworkInput{ID: "n-123", Name: "lan", Bridge: "jo0", Spec: n})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "plan_network_routed", DescribePlan(steps))
	golden(t, "plan_network_routed_rollback", DescribePlan(Rollback(steps)))

	p := api.NetworkSpec{Uplink: "em0", IPv6: &api.IPv6Net{Mode: api.IPv6Proxy, Subnet: "2001:db8:2::/64", Gateway: "fe80::1", Range: "2001:db8:2::100-2001:db8:2::200"}}
	steps, err = NetworkPlan(NetworkInput{ID: "n-456", Name: "wan6", Bridge: "jo1", Spec: p})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "plan_network_proxy", DescribePlan(steps))
	for _, s := range steps {
		if s.Op == OpAddAddr && strings.HasPrefix(s.Arg, "2001:") {
			t.Fatalf("proxy bridge got an on-link copy: %s", s.Arg)
		}
	}

	a := AttachInput{JailID: "j-1", JailUUID: "0b0e7f3e-5c4b-4f6e-9d3e-111111111111", Index: 0, Bridge: "jo0", MTU: 1500, Private: true,
		IPv4: pfx("10.20.0.10/24"), IPv6: pfx("2001:db8:1::100/64"), Gateway4: ip("10.20.0.1"), Gateway6: ip("fe80::1")}
	steps, err = AttachPlan(a)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "plan_attach_freebsd", DescribePlan(steps))
	a.IPv6Mode, a.Uplink, a.IPv6, a.HostConfigured = api.IPv6Proxy, "em0", pfx("2001:db8:2::100/128"), true
	steps, err = AttachPlan(a)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "plan_attach_linux_proxy", DescribePlan(steps))
	golden(t, "plan_attach_linux_proxy_rollback", DescribePlan(Rollback(steps)))
	if (Step{Op: "zzz"}).Describe() != "zzz" {
		t.Fatal("unknown op")
	}
}

func TestPlanErrors(t *testing.T) {
	good := api.NetworkSpec{IPv4: &api.IPv4Net{Subnet: "10.20.0.0/24", Gateway: "10.20.0.1", Range: "10.20.0.10-10.20.0.20"}}
	for _, in := range []NetworkInput{
		{ID: "x", Name: "Bad Name", Bridge: "jo0", Spec: good},
		{ID: "", Name: "lan", Bridge: "jo0", Spec: good},
		{ID: "a b", Name: "lan", Bridge: "jo0", Spec: good},
		{ID: "x", Name: "lan", Bridge: "", Spec: good},
		{ID: "x", Name: "lan", Bridge: "jo0;", Spec: good},
		{ID: "x", Name: "lan", Bridge: "jo0", Spec: api.NetworkSpec{}},
	} {
		if _, err := NetworkPlan(in); err == nil {
			t.Errorf("accepted %+v", in)
		}
	}
	ok := AttachInput{JailID: "j", JailUUID: "u", Bridge: "jo0"}
	if _, err := AttachPlan(ok); err != nil {
		t.Fatal(err)
	}
	for _, mut := range []func(*AttachInput){
		func(a *AttachInput) { a.JailID = "" },
		func(a *AttachInput) { a.Bridge = "x y" },
		func(a *AttachInput) { a.Index = -1 },
		func(a *AttachInput) { a.JailUUID = "" },
		func(a *AttachInput) { a.MAC = "ZZ" },
		func(a *AttachInput) { a.MTU = 3 },
		func(a *AttachInput) { a.IPv4 = pfx("2001:db8::1/64") },
		func(a *AttachInput) { a.Gateway6 = ip("10.0.0.1") },
		func(a *AttachInput) { a.IPv6Mode, a.IPv6, a.Uplink = api.IPv6Proxy, pfx("2001:db8::1/128"), "" },
	} {
		a := ok
		mut(&a)
		if _, err := AttachPlan(a); err == nil {
			t.Errorf("accepted %+v", a)
		}
	}
}

func TestJSONCanonical(t *testing.T) {
	j := api.Jail{
		Metadata: api.ObjectMeta{Name: "web", UUID: "u-1", Scope: api.ScopeHost, Generation: 3,
			Labels:    map[string]string{"z": "1", "a": "<2>"},
			CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("x", 3600))},
		Spec: api.JailSpec{Image: "freebsd-15.0@stable", Env: map[string]string{"B": "2", "A": "1"},
			Network: []api.NetworkAttach{{Network: "lan", IPv4: "10.20.0.10"}}},
		Status: api.JailStatus{Power: api.PowerRunning, JID: 7},
	}
	a, err := SpecJSON(j)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := SpecJSON(j)
	if string(a) != string(b) || !strings.HasSuffix(string(a), "}\n") || strings.HasSuffix(string(a), "\n\n") {
		t.Fatal("not stable or bad newline")
	}
	if strings.Contains(string(a), "running") || strings.Contains(string(a), `"jid"`) {
		t.Fatal("status leaked into spec.json")
	}
	if !strings.Contains(string(a), "<2>") {
		t.Fatal("html escaped")
	}
	golden(t, "spec_json", string(a))
	bj, err := BundleJSON(api.RestoreBundle{Spec: j.Spec, ImageGeneration: "freebsd-15.0#g3", ImageManifest: "sha256:abc",
		Networks: []api.NetworkSpec{{Uplink: "em0"}}, Volumes: []string{"data"}, SecretRefs: []string{"s1"}})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "bundle_json", string(bj))
	// key order is alphabetical at every level
	if strings.Index(string(a), `"apiVersion"`) > strings.Index(string(a), `"kind"`) {
		t.Fatal("keys not sorted")
	}
}

func TestLinuxDevfsRuleset(t *testing.T) {
	n, text, err := LinuxDevfsRuleset(nil)
	if err != nil || n < 5000 || n >= 55000 {
		t.Fatalf("%d %v", n, err)
	}
	golden(t, "devfs_linux", text)
	n2, text2, _ := LinuxDevfsRuleset([]string{"tun", "tun"})
	if n2 == n || !strings.Contains(text2, "add path 'tun*' unhide") {
		t.Fatal(n2, text2)
	}
	if n3, _, _ := LinuxDevfsRuleset([]string{"tun"}); n3 != n2 {
		t.Fatal("the number must follow the device set")
	}
	if _, _, err := LinuxDevfsRuleset([]string{"kmem"}); err == nil {
		t.Fatal("kmem accepted")
	}
	// The mount order is the verified one and the ruleset is named.
	fs, _ := Fstab(FstabInput{OS: api.OSLinux, Root: "/r", DevfsRuleset: n})
	var kinds []string
	for _, l := range strings.Split(fs, "\n") {
		if f := strings.Fields(l); len(f) > 3 && !strings.HasPrefix(l, "#") {
			kinds = append(kinds, f[0]+":"+strings.TrimPrefix(f[1], "/r"))
		}
	}
	want := "devfs:/dev fdescfs:/dev/fd /r:/compat/linux tmpfs:/compat/linux/dev/shm linprocfs:/proc linsysfs:/sys"
	if strings.Join(kinds, " ") != want {
		t.Fatalf("%s", strings.Join(kinds, " "))
	}
}
