package thinupgrade

import (
	"reflect"
	"strings"
	"testing"
)

func TestPersistentPaths(t *testing.T) {
	got := MountList(PersistentPaths(DefaultVarPaths))
	want := "/etc /usr/local /var/db /var/log /var/mail /var/spool /home /root"
	if got != want {
		t.Fatal(got)
	}
	p := PersistentPaths([]string{"db/ports", " /log/ "})
	if p[2].Name != "var-db-ports" || p[3].Mount != "/var/log" {
		t.Fatalf("%+v", p)
	}
}

func TestParseWritten(t *testing.T) {
	if n, err := ParseWritten("12345\n"); err != nil || n != 12345 {
		t.Fatal(n, err)
	}
	if _, err := ParseWritten("-"); err == nil {
		t.Fatal("dash must fail")
	}
	if HumanBytes(2048) != "2.0 KiB" || HumanBytes(5) != "5 B" {
		t.Fatal(HumanBytes(2048))
	}
}

func TestVersions(t *testing.T) {
	a, _ := ParseVersion("15.0-RELEASE-p2")
	b, _ := ParseVersion("15.1-STABLE")
	c, _ := ParseVersion("16.0")
	if a.Major != 15 || !a.Less(b) || !KernelTooOld(a, b) || KernelTooOld(b, a) {
		t.Fatal(a, b)
	}
	if ch, _ := MajorChange(a, b); ch {
		t.Fatal("minor is not major")
	}
	if ch, n := MajorChange(b, c); !ch || !strings.Contains(n, "MAJOR") {
		t.Fatal(n)
	}
	if _, err := ParseVersion("x"); err == nil {
		t.Fatal("expected error")
	}
}

func TestMerge3Passwd(t *testing.T) {
	base := []string{"root:*:0:0", "daemon:*:1:1"}
	ours := []string{"root:*:0:0", "daemon:*:1:1", "alice:*:3001:3001"}
	theirs := []string{"root:*:0:0", "daemon:*:1:1:gecos", "bob:*:3002:3002"}
	r := Merge3Keyed(base, ours, theirs, PasswdKey)
	if len(r.Conflicts) != 0 || !reflect.DeepEqual(r.Lines, []string{"root:*:0:0", "daemon:*:1:1:gecos", "alice:*:3001:3001", "bob:*:3002:3002"}) {
		t.Fatalf("%+v", r)
	}
	ours[1] = "daemon:*:1:1:mine"
	r = Merge3Keyed(base, ours, theirs, PasswdKey)
	if !reflect.DeepEqual(r.Conflicts, []string{"daemon"}) {
		t.Fatalf("%+v", r)
	}
}

func TestMerge3RC(t *testing.T) {
	base := []string{"# c", "sshd_enable=\"NO\"", "hostname=\"a\""}
	ours := []string{"# c", "sshd_enable=\"YES\"", "hostname=\"a\""}
	theirs := []string{"# c", "sshd_enable=\"NO\"", "hostname=\"b\"", "newvar=\"1\""}
	r := Merge3Keyed(base, ours, theirs, RCKey)
	if len(r.Conflicts) != 0 || strings.Join(r.Lines, "|") != "# c|sshd_enable=\"YES\"|hostname=\"b\"|newvar=\"1\"" {
		t.Fatalf("%+v", r)
	}
	theirs[1] = "sshd_enable=\"MAYBE\""
	r = Merge3Keyed(base, ours, theirs, RCKey)
	if len(r.Conflicts) != 1 || r.Conflicts[0] != "sshd_enable" {
		t.Fatalf("%+v", r)
	}
	// deletion on one side is honoured when the other side did not touch the key
	r = Merge3Keyed(base, ours, []string{"# c", "sshd_enable=\"NO\""}, RCKey)
	if len(r.Conflicts) != 0 || len(r.Lines) != 2 {
		t.Fatalf("%+v", r)
	}
}

func TestConflictMarkers(t *testing.T) {
	if !HasConflictMarkers("a\n<<<<<<< x\nb\n=======\nc\n>>>>>>> y\n") || HasConflictMarkers("a\n<<<<<<< x\n") || HasConflictMarkers("ok") {
		t.Fatal("markers")
	}
}

func TestParseEtcupdateStatus(t *testing.T) {
	out := "Warnings:\n  Modified regular file remains: /etc/foo\n\nConflicts remaining:\n  /etc/master.passwd\n  /etc/hosts\n\nNeeds update:\n  /etc/bar\n C /etc/syslog.conf\n"
	got := ParseEtcupdateStatus(out)
	want := []string{"/etc/hosts", "/etc/master.passwd", "/etc/syslog.conf"}
	if !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	if len(ParseEtcupdateStatus("")) != 0 {
		t.Fatal("empty")
	}
}

func TestDrift(t *testing.T) {
	mounts := []string{"/var/db", "/var/log", "/etc"}
	old := []string{"/var", "/var/db", "/var/db/pkg", "/var/log", "/var/run", "/etc", "/etc/rc.d"}
	nw := []string{"/var", "/var/db", "/var/db/pkg", "/var/db/newthing", "/var/log", "/var/run", "/var/run/new", "/etc", "/etc/rc.d", "/etc/newd"}
	d := DriftDirs(old, nw, mounts)
	if !reflect.DeepEqual(d.Added, []string{"/etc/newd", "/var/db/newthing"}) || len(d.Removed) != 0 {
		t.Fatalf("%+v", d)
	}
	d = DriftDirs(nw, old, mounts)
	if len(d.Removed) != 2 || len(d.Added) != 0 {
		t.Fatalf("%+v", d)
	}
}

func TestRendering(t *testing.T) {
	if !strings.Contains(RepoConf("FreeBSD-base", "pkg+https://x/${ABI}/base", true), "mirror_type") ||
		strings.Contains(RepoConf("r", "file:///d", false), "srv") {
		t.Fatal("repo conf")
	}
	if !strings.Contains(JailConf("jop2a", "/jop-proto-2/jail/root"), `path = "/jop-proto-2/jail/root";`) {
		t.Fatal("jail conf")
	}
	a := PkgArgs("/r", "/d", "FreeBSD:15:amd64", "upgrade", "-y")
	if a[0] != "pkg" || a[len(a)-1] != "-y" || !strings.Contains(strings.Join(a, " "), "ABI=FreeBSD:15:amd64") {
		t.Fatal(a)
	}
	ok, lost := SurvivalSummary([]Finding{{"/a", true}, {"/b", false}})
	if len(ok) != 1 || len(lost) != 1 {
		t.Fatal()
	}
}
