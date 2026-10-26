package lifecycle

import (
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
)

// withStorages adds a second storage and a locator that sends jails of the
// project "p1" there.
func withStorages(t *testing.T, w *world) Storage {
	t.Helper()
	fast := Storage{Name: "fast", Root: "tank/fast", MountBase: "/fast"}
	for _, d := range []string{"tank/fast", "tank/fast/images", "tank/fast/projects", "tank/fast/projects/p1", "tank/fast/projects/p1/jails"} {
		if err := w.Z.Create(bg, d, map[string]string{"jailops:managed": "on"}); err != nil {
			t.Fatal(err)
		}
	}
	def := Storage{Name: "default", Root: "tank/jailops", MountBase: "/jailops"}
	w.S.Cfg.Locate = func(key string) (Storage, bool) {
		if strings.HasPrefix(key, "p1_") {
			return fast, true
		}
		return def, true
	}
	w.S.Cfg.Storages = func() []Storage { return []Storage{def, fast} }
	return fast
}

// A jail of a project lives below <storage>/projects/<project>/jails/<name>,
// under its qualified key, with the project in its properties and record.
func TestProjectJailLayoutOnAnotherStorage(t *testing.T) {
	w := newWorld(t)
	withStorages(t, w)
	j := w.jail("web")
	j.Metadata.Project = "p1"
	if _, err := w.S.CreateJail(bg, j, false); err != nil {
		t.Fatal(err)
	}
	c := "tank/fast/projects/p1/jails/web"
	for _, ds := range []string{c, c + "/root", c + "/etc"} {
		if ok, _ := w.Z.Exists(bg, ds); !ok {
			t.Fatalf("%s is missing", ds)
		}
	}
	if ok, _ := w.Z.Exists(bg, "tank/jailops/projects/alpha/jails/web"); ok {
		t.Fatal("the jail was created on the default storage")
	}
	if got := w.Z.PropOrEmpty(c+"/root", "mountpoint"); got != "/fast/projects/p1/jails/web/root" {
		t.Fatalf("mountpoint %q", got)
	}
	if w.Z.PropOrEmpty(c, PropProject) != "p1" || w.Z.PropOrEmpty(c, PropName) != "web" {
		t.Fatalf("props: %q %q", w.Z.PropOrEmpty(c, PropProject), w.Z.PropOrEmpty(c, PropName))
	}
	b, _ := w.Files.ReadFile(bg, "/fast/projects/p1/jails/web/spec.json")
	if !strings.Contains(string(b), `"project": "p1"`) || !strings.Contains(string(b), `"name": "web"`) {
		t.Fatalf("spec.json:\n%s", b)
	}
	// The generation is asked for on the pool of the jail.
	if len(w.Images.roots) == 0 || w.Images.roots[0] != "tank/fast" {
		t.Fatalf("roots %v", w.Images.roots)
	}
	// Operations take the key.
	if err := w.S.StartJail(bg, "p1_web"); err != nil {
		t.Fatal(err)
	}
	if up, _ := w.Jails.Running(bg, "p1_web"); !up {
		t.Fatal("the OS-level name is the qualified key")
	}
	if err := w.S.StopJail(bg, "p1_web"); err != nil {
		t.Fatal(err)
	}
	// The same name in the default project is another jail.
	if _, err := w.S.CreateJail(bg, w.jail("web"), false); err != nil {
		t.Fatal(err)
	}
	if ok, _ := w.Z.Exists(bg, "tank/jailops/projects/alpha/jails/web"); !ok {
		t.Fatal("the default project's jail is missing")
	}
	// Discovery finds both, rebuilds both, and a rebuilt record names its project.
	found, err := w.S.Discover(bg)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]string{}
	for _, f := range found {
		keys[f.Key] = f.Storage.Name
	}
	if len(keys) != 2 || keys["p1_web"] != "fast" || keys["alpha_web"] != "default" {
		t.Fatalf("%v", keys)
	}
	recs, errs, err := w.S.RebuildAll(bg)
	if err != nil || len(errs) != 0 || len(recs) != 2 {
		t.Fatalf("%v %v %d", err, errs, len(recs))
	}
	for _, r := range recs {
		if len(r.Problems) != 0 {
			t.Fatalf("%v", r.Problems)
		}
		if r.Jail.Metadata.Name != "web" || (r.Jail.Metadata.Project != "p1" && r.Jail.Metadata.Project != "alpha") {
			t.Fatalf("%+v", r.Jail.Metadata)
		}
	}
	// A snapshot and a copy stay on the jail's storage and project.
	if _, err := w.S.SnapshotJail(bg, "p1_web", "s1", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := w.S.CopyJail(bg, "p1_web", "p1_web2", "uuid-web2", "1", false); err != nil {
		t.Fatal(err)
	}
	if ok, _ := w.Z.Exists(bg, "tank/fast/projects/p1/jails/web2/root"); !ok {
		t.Fatal("the copy is not next to the original")
	}
	b, _ = w.Files.ReadFile(bg, "/fast/projects/p1/jails/web2/spec.json")
	if !strings.Contains(string(b), `"name": "web2"`) || !strings.Contains(string(b), `"project": "p1"`) {
		t.Fatalf("%s", b)
	}
	// Destroying removes the datasets of that storage only.
	if _, err := w.S.DestroyJail(bg, "p1_web", false, false); err != nil {
		t.Fatal(err)
	}
	if ok, _ := w.Z.Exists(bg, c); ok {
		t.Fatal("not destroyed")
	}
	if ok, _ := w.Z.Exists(bg, "tank/jailops/projects/alpha/jails/web"); !ok {
		t.Fatal("the other project's jail was destroyed")
	}
}

// A name that is not valid, or an unqualified key of another project, is refused.
func TestKeysAreValidated(t *testing.T) {
	w := newWorld(t)
	j := w.jail("web")
	j.Metadata.Project = "p_1" // an underscore would make keys ambiguous
	_, err := w.S.CreateJail(bg, j, false)
	wantCode(t, err, api.CodeInvalid)
	j = w.jail(strings.Repeat("a", 40))
	j.Metadata.Project = strings.Repeat("b", 40)
	_, err = w.S.CreateJail(bg, j, false)
	wantCode(t, err, api.CodeInvalid)
	if _, err := w.S.CreateJail(bg, w.jail("web"), false); err != nil {
		t.Fatal(err)
	}
	// A dataset of the default project is never found under another project.
	wantCode(t, w.S.StopJail(bg, "p1_web"), api.CodeNotFound)
}
