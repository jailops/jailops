package hostbackup

import (
	"path"
	"strings"
	"testing"
)

func FuzzSafeRel(f *testing.F) {
	for _, s := range []string{".", "etc", "data/db", "../x", "/abs", "a//b", "a/./b", "a/../b", "x@y", "a b", "", "a\x00b", "data/"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, rel string) {
		if !safeRel(rel) {
			return
		}
		if rel == "." {
			return
		}
		for _, c := range strings.Split(rel, "/") {
			if c == "" || c == "." || c == ".." {
				t.Fatalf("%q accepted with component %q", rel, c)
			}
		}
		if strings.HasPrefix(rel, "/") || strings.ContainsAny(rel, "@#\x00") {
			t.Fatalf("%q accepted", rel)
		}
		// a safe name never leaves the container
		got := path.Join("/jo/web", rel)
		if !strings.HasPrefix(got, "/jo/web/") {
			t.Fatalf("%q escapes: %q", rel, got)
		}
	})
}

func FuzzParseSnapTime(f *testing.F) {
	for _, s := range []string{"jailops-backup-20260101T000000-ab12", "jailops-backup-", "jailops-backup-2026", "daily", "jailops-backup-99999999T999999-x"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, name string) {
		tm := parseSnapTime(name)
		if tm.IsZero() {
			return
		}
		if !strings.HasPrefix(name, SnapshotPrefix) {
			t.Fatalf("%q parsed without the prefix", name)
		}
		rest := strings.TrimPrefix(name, SnapshotPrefix)
		if tm.UTC().Format("20060102T150405") != rest[:15] {
			t.Fatalf("%q parsed as %v", name, tm)
		}
	})
}

func FuzzDatasetProps(f *testing.F) {
	f.Add("quota", "1G", "root/etc", "etc")
	f.Add("mountpoint", "/", "../../x", "..")
	f.Add("jailops:path", "/etc", ".", ".")
	f.Fuzz(func(t *testing.T, k, v, mrel, rel string) {
		pl := Placement{Container: "tank/j", Mountpoint: "/jo/j"}
		rec := map[string]string{k: v, "jailops:rel": "x", "jailops:id": "evil", "mountpoint": "/etc"}
		out := datasetProps(rec, pl, rel, mrel, map[string]string{pID: "mine", pManaged: "on"})
		if out[pID] != "mine" || out[pManaged] != "on" {
			t.Fatalf("recorded properties overrode ownership: %v", out)
		}
		if _, ok := out["jailops:rel"]; ok {
			t.Fatal("jailops:rel leaked")
		}
		for key := range out {
			if !strings.HasPrefix(key, "jailops:") && !nativeProps[key] && key != "mountpoint" {
				t.Fatalf("property %q is not allowed", key)
			}
		}
		if safeRel(rel) && (mrel == "" || mrel == "." || safeRel(mrel)) && !strings.HasPrefix(out["mountpoint"], "/jo/j") {
			t.Fatalf("mountpoint %q leaves the container (rel %q, mrel %q)", out["mountpoint"], rel, mrel)
		}
	})
}
