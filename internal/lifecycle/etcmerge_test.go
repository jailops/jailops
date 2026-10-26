package lifecycle

import (
	"errors"
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
)

func TestMerge3Keyed(t *testing.T) {
	base := []string{"root:x", "daemon:x", "# c"}
	for name, c := range map[string]struct {
		ours, theirs []string
		want         string
		conflicts    string
	}{
		"unchanged":        {base, base, "root:x daemon:x # c", ""},
		"both add":         {append([]string{}, append(base, "app:1")...), append([]string{}, append(base, "svc:1")...), "root:x daemon:x # c app:1 svc:1", ""},
		"ours edits":       {[]string{"root:y", "daemon:x", "# c"}, base, "root:y daemon:x # c", ""},
		"theirs edits":     {base, []string{"root:x", "daemon:z", "# c"}, "root:x daemon:z # c", ""},
		"theirs removes":   {base, []string{"root:x", "# c"}, "root:x # c", ""},
		"both edit equal":  {[]string{"root:y", "daemon:x", "# c"}, []string{"root:y", "daemon:x", "# c"}, "root:y daemon:x # c", ""},
		"both edit differ": {[]string{"root:a", "daemon:x", "# c"}, []string{"root:b", "daemon:x", "# c"}, "root:a daemon:x # c", "root"},
		"edit vs remove":   {[]string{"root:a", "daemon:x", "# c"}, []string{"daemon:x", "# c"}, "root:a daemon:x # c", "root"},
	} {
		r := Merge3Keyed(base, c.ours, c.theirs, PasswdKey)
		if got := strings.Join(r.Lines, " "); got != c.want || strings.Join(r.Conflicts, ",") != c.conflicts {
			t.Errorf("%s: %q conflicts %v", name, got, r.Conflicts)
		}
	}
}

func TestParseEtcupdate(t *testing.T) {
	c, m := ParseEtcupdate([]byte("Conflicts remain:\n  C /etc/ssh/sshd_config\n  M /etc/motd\nC /etc/master.passwd\nnoise C /etc/x\n"))
	if strings.Join(c, ",") != "/etc/ssh/sshd_config,/etc/master.passwd" || strings.Join(m, ",") != "/etc/motd" {
		t.Fatalf("%v %v", c, m)
	}
}

const (
	gen1Root = "/jailops/images/fb/g1"
	gen2Root = "/jailops/images/fb/g2"
	jRoot    = "/jailops/projects/alpha/jails/web/root"
	jNewRoot = "/jailops/projects/alpha/jails/web/root.new"
)

func (w *world) accounts(root, master, group string) {
	w.Files.Data[root+"/etc/master.passwd"] = []byte(master)
	w.Files.Data[root+"/etc/group"] = []byte(group)
}

func TestEtcAfterUpgradeThenRootRollbackKeepsJailLocalEdits(t *testing.T) {
	w := runningSetup(t)
	g1 := "root:*:0\ndaemon:*:1\n"
	g2 := "root:*:0\ndaemon:*:1\nnewsvc:*:900\n"
	w.accounts(gen1Root, g1, "wheel:*:0\n")
	w.accounts(gen2Root, g2, "wheel:*:0\nnewgrp:*:900\n")
	// The jail added a user of its own after creation.
	w.accounts(jRoot, g1+"app:*:1001\n", "wheel:*:0\n")
	w.accounts(jNewRoot, g2, "wheel:*:0\nnewgrp:*:900\n") // what the clone of g2@ro holds
	w.Files.Data[jRoot+"/etc/rc.conf"] = []byte("hostname=\"web\"\n")

	if _, err := upgrade(w, api.JailUpgrade{Generation: "g2"}); err != nil {
		t.Fatal(err)
	}
	if got := string(w.Files.Data[jRoot+"/etc/master.passwd"]); got != "root:*:0\ndaemon:*:1\napp:*:1001\nnewsvc:*:900\n" {
		t.Fatalf("master.passwd after upgrade:\n%s", got)
	}
	if got := string(w.Files.Data[jRoot+"/etc/group"]); got != "wheel:*:0\nnewgrp:*:900\n" {
		t.Fatalf("group after upgrade:\n%s", got)
	}
	cmds := strings.Join(w.Exec.Commands(), "\n")
	for _, want := range []string{
		"tar -C " + gen1Root + " -cf /jailops/projects/alpha/jails/web/.merge-100/old-etc.tar ./etc",
		"etcupdate extract -d /jailops/projects/alpha/jails/web/.merge-100/etcupdate -t /jailops/projects/alpha/jails/web/.merge-100/old-etc.tar -D " + jRoot,
		"etcupdate -d /jailops/projects/alpha/jails/web/.merge-100/etcupdate -t /jailops/projects/alpha/jails/web/.merge-100/new-etc.tar -D " + jRoot + " -L ",
		"chmod 600 " + jRoot + "/etc/master.passwd",
		"pwd_mkdb -p -d " + jRoot + "/etc " + jRoot + "/etc/master.passwd",
	} {
		if !strings.Contains(cmds, want) {
			t.Errorf("no %q in\n%s", want, cmds)
		}
	}
	if string(w.Files.Data[jRoot+"/etc/rc.conf"]) != "hostname=\"web\"\n" {
		t.Error("rc.conf was touched")
	}
	for p := range w.Files.Data {
		if strings.Contains(p, ".merge-") {
			t.Errorf("work file left behind: %s", p)
		}
	}

	// A jail-local edit between the upgrade and the rollback.
	w.Files.Data[jRoot+"/etc/master.passwd"] = []byte(string(w.Files.Data[jRoot+"/etc/master.passwd"]) + "bob:*:1002\n")
	w.accounts(jNewRoot, g1, "wheel:*:0\n") // the clone of g1@ro the rollback merges from
	w.Exec.Calls = nil
	if _, err := rollback(w, RollbackOptions{}); err != nil {
		t.Fatal(err)
	}
	// Reverse direction: g2 is the baseline, g1 the target. The service
	// that only g2 had goes away, the local users stay.
	if got := string(w.Files.Data[jRoot+"/etc/master.passwd"]); got != "root:*:0\ndaemon:*:1\napp:*:1001\nbob:*:1002\n" {
		t.Fatalf("master.passwd after rollback:\n%s", got)
	}
	if got := string(w.Files.Data[jRoot+"/etc/group"]); got != "wheel:*:0\n" {
		t.Fatalf("group after rollback:\n%s", got)
	}
	rb := strings.Join(w.Exec.Commands(), "\n")
	if !strings.Contains(rb, "tar -C "+gen2Root+" -cf ") || !strings.Contains(rb, "pwd_mkdb -p") {
		t.Fatalf("the reverse merge did not use g2 as its baseline:\n%s", rb)
	}
}

func TestEtcAccountConflictsAreRefusedAndChangeNothing(t *testing.T) {
	w := runningSetup(t)
	w.accounts(gen1Root, "root:*:0\napp:*:1001\n", "wheel:*:0\n")
	w.accounts(gen2Root, "root:*:0\napp:*:2000\n", "wheel:*:0\n")
	w.accounts(jRoot, "root:*:0\napp:*:1500\n", "wheel:*:0\n")
	w.accounts(jNewRoot, "root:*:0\napp:*:2000\n", "wheel:*:0\n")
	before := w.state()
	_, err := upgrade(w, api.JailUpgrade{Generation: "g2"})
	var ae *api.Error
	if !errors.As(err, &ae) || ae.Code != api.CodeConflict || ae.Detail["files"] != "/etc/master.passwd:app" {
		t.Fatalf("%v", err)
	}
	if got := string(w.Files.Data[jRoot+"/etc/master.passwd"]); got != "root:*:0\napp:*:1500\n" {
		t.Fatalf("the conflicting file was rewritten: %q", got)
	}
	if w.state() != before || !w.Jails.IsUp("alpha_web") {
		t.Fatal("a conflict changed the pool or stopped the jail")
	}
}

func TestEtcupdateConflictsInAccountFilesAreIgnored(t *testing.T) {
	w := runningSetup(t)
	w.accounts(gen1Root, "root:*:0\n", "wheel:*:0\n")
	w.accounts(gen2Root, "root:*:0\nsvc:*:9\n", "wheel:*:0\n")
	w.accounts(jRoot, "root:*:0\napp:*:5\n", "wheel:*:0\n")
	w.accounts(jNewRoot, "root:*:0\nsvc:*:9\n", "wheel:*:0\n")
	// etcupdate trips over both users added to master.passwd, exits non-zero,
	// and that alone is not a conflict: the keyed merge resolves it.
	w.mergeOut = "C /etc/master.passwd\nM /etc/motd\n"
	w.mergeErr = errors.New("exit status 1")
	if _, err := upgrade(w, api.JailUpgrade{Generation: "g2"}); err != nil {
		t.Fatal(err)
	}
	if got := string(w.Files.Data[jRoot+"/etc/master.passwd"]); got != "root:*:0\napp:*:5\nsvc:*:9\n" {
		t.Fatalf("%q", got)
	}
}
