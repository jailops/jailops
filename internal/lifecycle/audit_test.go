package lifecycle

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"jailops.net/jailops/internal/jailaudit"
)

// The audit of a running jail reads it and changes nothing: the jail runs on, no snapshot, the helper gets the files of
// the base packages with the checksums of the programs only, and the package database is read from a copy.
func TestAuditRootOfARunningJail(t *testing.T) {
	w := thickRunning(t)
	w.Files.Audit = jailaudit.Facts{Verified: 1}
	before := w.state()
	w.Exec.Calls = nil
	answer := w.Exec.Handler
	w.Exec.Handler = func(argv []string) ([]byte, error) {
		if slices.Contains(argv, "audit") {
			// pkg audit exits 1 when it names a package
			return []byte("nginx-1.0 is vulnerable:\n  nginx -- x\n  CVE: CVE-2026-1\n  WWW: https://vuxml.FreeBSD.org/freebsd/x.html\n\n1 problem(s) in 1 package(s) found.\n"), errors.New("exit status 1")
		}
		if answer == nil {
			return nil, nil
		}
		return answer(argv)
	}
	ra, running, err := w.S.AuditRoot(bg, "alpha_web", true)
	if err != nil {
		t.Fatal(err)
	}
	if !ra.VulnRan || len(ra.Vulns) != 1 || ra.Vulns[0].Name != "nginx" || !slices.Equal(ra.Vulns[0].IDs, []string{"CVE-2026-1"}) {
		t.Fatalf("pkg audit: %+v %q", ra.Vulns, ra.VulnErr)
	}
	if !running || !ra.HasPkgDB || ra.Facts.Verified != 1 || !ra.Owned["/usr/local/sbin/nginx"] || ra.Packages == 0 || ra.BasePackages == 0 {
		t.Fatalf("%+v running=%v", ra, running)
	}
	if want := []jailaudit.File{{Path: "/bin/sh", Sum: "1$aa"}, {Path: "/etc/rc"}}; !slices.Equal(w.Files.Audited, want) {
		t.Fatalf("files: %+v", w.Files.Audited)
	}
	audited := false
	for _, c := range pkgCalls(w) {
		if strings.Contains(c, "-r "+rootMPt) || !strings.Contains(c, "audit") || !strings.Contains(c, " query ") && !strings.HasSuffix(c, " audit -f "+jailaudit.HostVulnXML) {
			t.Errorf("pkg did more than read a copy of the database: %s", c)
		}
		audited = audited || strings.HasSuffix(c, " audit -f "+jailaudit.HostVulnXML)
	}
	if !audited {
		t.Errorf("no pkg audit: %v", pkgCalls(w))
	}
	if !w.Jails.IsUp("alpha_web") || w.state() != before {
		t.Fatal("the audit changed the jail")
	}
}

// A stopped jail's root is mounted for the read and unmounted after it.
func TestAuditRootOfAStoppedJail(t *testing.T) {
	w := thickRunning(t)
	if err := w.S.StopJail(bg, "alpha_web"); err != nil {
		t.Fatal(err)
	}
	before := w.state()
	w.Exec.Calls = nil
	if _, running, err := w.S.AuditRoot(bg, "alpha_web", false); err != nil || running {
		t.Fatal(running, err)
	}
	for _, c := range pkgCalls(w) {
		if strings.Contains(c, " audit -f") {
			t.Errorf("pkg audit without the host's database: %s", c)
		}
	}
	if w.state() != before || w.S.prop(bg, jc+"/root", "mounted") == "yes" {
		t.Fatal("the audit left the stopped jail changed or mounted")
	}
}
