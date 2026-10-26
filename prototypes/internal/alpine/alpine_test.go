package alpine

import (
	"strings"
	"testing"
)

const yaml = `---
-
  branch: v3.22
  arch: x86_64
  version: 3.22.1
  flavor: alpine-standard
  file: alpine-standard-3.22.1-x86_64.iso
  sha256: aaaa
-
  title: "Mini root filesystem"
  file: alpine-minirootfs-3.22.1-x86_64.tar.gz
  sha256: ` + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" + `
-
  file: alpine-minirootfs-3.22.1-aarch64.tar.gz
  sha256: bbbb
`

func TestParseLatest(t *testing.T) {
	r, err := ParseLatestReleases(yaml, "x86_64")
	if err != nil || r.File != "alpine-minirootfs-3.22.1-x86_64.tar.gz" || !ValidSHA256(r.SHA256) {
		t.Fatalf("%+v %v", r, err)
	}
	r, err = ParseLatestReleases(yaml, "aarch64")
	if err != nil || r.SHA256 != "bbbb" {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := ParseLatestReleases(yaml, "riscv64"); err == nil {
		t.Fatal("want error")
	}
}

func TestBranch(t *testing.T) {
	if BranchOf("3.22.1") != "v3.22" || BranchOf("v3.21") != "v3.21" {
		t.Fatal()
	}
}

func TestMountBase(t *testing.T) {
	for p, ok := range map[string]bool{"/jop-proto-6": true, "/mnt/jop6": true, "/": false, "/etc": false, "jop": false, "/jop/../etc": false, "/a/jop x": false} {
		if (ValidMountBase(p) == nil) != ok {
			t.Errorf("%s", p)
		}
	}
	if !Under("/a/b", "/a/b/c") || Under("/a/b", "/a/b") || Under("/a/b", "/a/bc") {
		t.Fatal("Under")
	}
}

func TestJailConf(t *testing.T) {
	s, err := RenderJailConf(JailParams{Name: JailName, Path: `/jop/al"pine$x`, Hostname: "h", VNet: true, DevfsRuleset: 6600})
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{`path = "/jop/al\"pine\$x";`, "vnet;", "devfs_ruleset = 6600;", "jop6alpine {"} {
		if !strings.Contains(s, w) {
			t.Errorf("missing %q in\n%s", w, s)
		}
	}
	if strings.Contains(s, "inherit") {
		t.Error("vnet conf must not inherit")
	}
	s, _ = RenderJailConf(JailParams{Name: JailName, Path: "/p", Hostname: "h", InheritIP: true})
	if !strings.Contains(s, "ip4 = inherit;") || strings.Contains(s, "vnet;") {
		t.Error(s)
	}
	for _, bad := range []JailParams{
		{Name: "other", Path: "/p", Hostname: "h", VNet: true},
		{Name: JailName, Path: "/p\n}", Hostname: "h", VNet: true},
		{Name: JailName, Path: "/p", Hostname: "h"},
	} {
		if _, err := RenderJailConf(bad); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}

func TestDevfsAndMounts(t *testing.T) {
	r := DevfsRules(6600)
	if r[0][len(r[0])-1] != "hide" || len(r) < 8 {
		t.Fatal(r)
	}
	if !RulesetInUse("0\n6600\n", 6600) || RulesetInUse("0\n4\n", 6600) {
		t.Fatal("RulesetInUse")
	}
	m := MountList(6600)
	if m[0].Type != "devfs" || m[3].Rel != "compat/linux/dev/shm" {
		t.Fatal(m)
	}
	if got := strings.Join(MountArgv("/r", m[0]), " "); got != "mount -t devfs -o ruleset=6600 devfs /r/dev" {
		t.Fatal(got)
	}
	out := "zroot/a / zfs rw 0 0\ndevfs /r/dev devfs rw 0 0\ntmpfs /r/dev/shm tmpfs rw 0 0\nlinprocfs /r/proc linprocfs rw 0 0\nx /rr/proc x rw 0 0\n"
	u := MountsUnder(out, "/r")
	if len(u) != 3 || u[0] != "/r/dev/shm" && u[0] != "/r/proc" {
		t.Fatal(u)
	}
	if u[len(u)-1] != "/r/dev" && u[len(u)-1] != "/r/proc" {
		t.Fatal(u)
	}
	if u[0] == "/r/dev" {
		t.Fatal("deepest first violated", u)
	}
}

func TestAddrs(t *testing.T) {
	out := `jop6j: flags=1008843<UP,BROADCAST,RUNNING,SIMPLEX,MULTICAST,LOWER_UP> metric 0 mtu 1500
	ether 02:00:00:00:06:0b
	inet 10.66.6.2 netmask 0xffffff00 broadcast 10.66.6.255
	inet6 fe80::ff:fe00:60b%jop6j prefixlen 64 scopeid 0x5
	inet6 fd66:6:6::2 prefixlen 64
`
	a := ParseAddrs(out)
	if len(a) != 2 || a[0] != "inet 10.66.6.2" || a[1] != "inet6 fd66:6:6::2" {
		t.Fatal(a)
	}
	if m := Missing(a, []string{"inet 10.66.6.2"}); len(m) != 1 || m[0] != "inet6 fd66:6:6::2" {
		t.Fatal(m)
	}
	if len(Missing(a, a)) != 0 {
		t.Fatal()
	}
}

func TestFailures(t *testing.T) {
	var f Failures
	if !strings.Contains(f.Table(), "no failed") {
		t.Fatal()
	}
	f.Add("apk", "jexec x apk", 1, strings.Repeat("x", 500))
	f.Add("apk", "b", 2, "a\nb")
	if !strings.Contains(f.Table(), "truncated") || !strings.Contains(f.Summary("apk"), "exit 2: a | b") || f.Summary("zz") != "" {
		t.Fatal(f.Table())
	}
}

func TestTemplates(t *testing.T) {
	if !strings.Contains(Detach("x"), "</dev/null") || !strings.Contains(Repositories("v3.22"), "v3.22/community") {
		t.Fatal()
	}
	if !strings.Contains(NginxConf(), "daemon on") || !strings.Contains(SshdConf(), "UsePAM no") {
		t.Fatal()
	}
	if SHA256Hex([]byte("")) != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatal()
	}
}
