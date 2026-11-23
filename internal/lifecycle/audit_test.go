package lifecycle

import (
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
	ra, running, err := w.S.AuditRoot(bg, "alpha_web")
	if err != nil {
		t.Fatal(err)
	}
	if !running || !ra.HasPkgDB || ra.Facts.Verified != 1 || !ra.Owned["/usr/local/sbin/nginx"] || ra.Packages == 0 || ra.BasePackages == 0 {
		t.Fatalf("%+v running=%v", ra, running)
	}
	if want := []jailaudit.File{{Path: "/bin/sh", Sum: "1$aa"}, {Path: "/etc/rc"}}; !slices.Equal(w.Files.Audited, want) {
		t.Fatalf("files: %+v", w.Files.Audited)
	}
	for _, c := range pkgCalls(w) {
		if strings.Contains(c, "-r "+rootMPt) || !strings.Contains(c, "audit") || !strings.Contains(c, " query ") {
			t.Errorf("pkg did more than read a copy of the database: %s", c)
		}
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
	if _, running, err := w.S.AuditRoot(bg, "alpha_web"); err != nil || running {
		t.Fatal(running, err)
	}
	if w.state() != before || w.S.prop(bg, jc+"/root", "mounted") == "yes" {
		t.Fatal("the audit left the stopped jail changed or mounted")
	}
}
