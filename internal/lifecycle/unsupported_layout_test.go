package lifecycle

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
)

// oldLayoutCreated makes, the way JailOps 0.9.2 made it, a stopped jail web with the root layout this release does not
// support: the container carries the layout property, the root is a clone of the generation fb#g1, and /etc,
// /usr/local and /var/db are datasets of their own mounted over it. This release does not make such a jail; it only
// finds them on a host it is upgraded on.
func (w *world) oldLayoutCreated() {
	w.t.Helper()
	id := "uuid-web"
	base := func(kind string, extra map[string]string) map[string]string {
		m := map[string]string{PropManaged: "on", PropID: id, PropKind: kind, PropName: "web", PropProject: "alpha"}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	must := func(err error) {
		w.t.Helper()
		if err != nil {
			w.t.Fatal(err)
		}
	}
	must(w.Z.Create(bg, jc, base(KindContainer, map[string]string{"mountpoint": jroot, PropLayout: OldLayoutValue, PropImage: "fb", PropComplete: "on"})))
	must(w.Z.Clone(bg, "tank/jailops/images/fb/g1@ro", jc+"/root", base(KindRoot, map[string]string{"mountpoint": jroot + "/root", PropGen: "1", PropImage: "fb"})))
	for _, p := range []struct{ ds, path string }{{"etc", "/etc"}, {"local", "/usr/local"}, {"var-db", "/var/db"}} {
		must(w.Z.Create(bg, jc+"/"+p.ds, base(KindPersistent, map[string]string{PropPath: p.path, "mountpoint": jroot + "/root" + p.path})))
	}
	w.Z.WriteFile(jc+"/etc", "/etc/rc.conf", "sshd_enable=YES")
	w.Z.WriteFile(jc+"/var-db", "/var/db/app/data", "rows")
	j := w.jail("web")
	rec := Record{Version: 1, Metadata: j.Metadata, Spec: j.Spec}
	rec.Metadata.Scope = api.ScopeHost
	spec := strings.Replace(string(marshal(rec)), `"image": "fb",`, `"image": "fb",`+"\n    \"layout\": \""+OldLayoutValue+"\",", 1)
	must(w.Files.WriteAtomic(bg, jroot+"/spec.json", []byte(spec)))
	must(w.Files.WriteAtomic(bg, jroot+"/bundle.json", w.S.bundleBytes(rec, "fb", "fb", 1, "sha256:m1", "tank/jailops/images/fb/g1", w.now)))
}

// oldLayoutRunning is that jail still running from before the upgrade: its datasets mounted and the jail up, as 0.9.2
// left it. This release never starts it.
func oldLayoutRunning(t *testing.T) *world {
	w := newWorld(t)
	w.oldLayoutCreated()
	for _, d := range []string{jc + "/root", jc + "/etc", jc + "/local", jc + "/var-db"} {
		if err := w.Z.Mount(bg, d); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Jails.Start(bg, "alpha_web"); err != nil {
		t.Fatal(err)
	}
	return w
}

// refusedForLayout checks that err is the refusal of an unsupported root layout and names the fix.
func refusedForLayout(t *testing.T, what string, err error) {
	t.Helper()
	if !api.IsCode(err, api.CodeUnsupported) || !strings.Contains(err.Error(), "root layout this release does not support") {
		t.Errorf("%s: %v", what, err)
		return
	}
	var ae *api.Error
	if !asAPI(err, &ae) || !strings.Contains(ae.Hint, "create a new jail and move its data over") || !strings.Contains(ae.Hint, "jailops jail convert web --layout thick") {
		t.Errorf("%s: the refusal does not name the fix: %+v", what, ae)
	}
}

// The datasets say that a jail of 0.9.2 has the old root layout, also without the layout property.
func TestUnsupportedLayoutIsRecognised(t *testing.T) {
	w := newWorld(t)
	w.oldLayoutCreated()
	r, err := w.S.Rebuild(bg, "alpha_web")
	if err != nil || !r.UnsupportedLayout || r.Jail.Metadata.Name != "web" {
		t.Fatalf("%+v %v", r, err)
	}
	if strings.Contains(string(mustJSON(t, r.Jail.Spec)), "layout") {
		t.Fatal("the spec read from the old jail's spec.json still names a layout")
	}
	w.Z.Inherit(bg, jc, PropLayout)
	if r, _ := w.S.Rebuild(bg, "alpha_web"); !r.UnsupportedLayout {
		t.Fatal("a container without the layout property but with persistent datasets is not recognised")
	}
	if lay, err := w.S.Layout(bg, "alpha_web"); err != nil || !lay.UnsupportedLayout {
		t.Fatalf("%+v %v", lay, err)
	}
}

// A jail with an unsupported root layout does not start; nothing is mounted or changed by the refusal.
func TestUnsupportedLayoutDoesNotStart(t *testing.T) {
	w := newWorld(t)
	w.oldLayoutCreated()
	before := w.state()
	refusedForLayout(t, "start", w.S.StartJail(bg, "alpha_web"))
	if w.Jails.IsUp("alpha_web") {
		t.Fatal("the jail was started")
	}
	if w.state() != before {
		t.Fatalf("the refused start changed the jail:\n%s", w.state())
	}
}

// A running jail with an unsupported root layout stops and can be deleted; its data is never touched by the stop.
func TestUnsupportedLayoutStopsAndIsDeleted(t *testing.T) {
	w := oldLayoutRunning(t)
	if err := w.S.StopJail(bg, "alpha_web"); err != nil {
		t.Fatal(err)
	}
	if w.Jails.IsUp("alpha_web") {
		t.Fatal("the jail still runs")
	}
	if v, _ := w.Z.ReadFile(jc+"/etc", "/etc/rc.conf"); v != "sshd_enable=YES" {
		t.Fatalf("/etc of the jail: %q", v)
	}
	if v, _ := w.Z.ReadFile(jc+"/var-db", "/var/db/app/data"); v != "rows" {
		t.Fatalf("/var/db of the jail: %q", v)
	}
	if o, _ := w.Z.Origin(bg, jc+"/root"); o != "tank/jailops/images/fb/g1@ro" {
		t.Fatalf("the root of the jail changed: origin %q", o)
	}
	if _, err := w.S.DestroyJail(bg, "alpha_web", false, false); err != nil {
		t.Fatal(err)
	}
	if ok, _ := w.Z.Exists(bg, jc); ok {
		t.Fatal("the jail was not deleted")
	}
	if ok, _ := w.Z.Exists(bg, "tank/jailops/images/fb/g1@ro"); !ok {
		t.Fatal("the generation went with the jail")
	}
}

// Every other operation refuses a jail with an unsupported root layout, names the fix, and changes nothing.
func TestUnsupportedLayoutOperationsAreRefused(t *testing.T) {
	w := oldLayoutRunning(t)
	before := w.state()
	ops := map[string]func() error{
		"upgrade":  func() error { _, err := upgrade(w, api.JailUpgrade{}); return err },
		"dry run":  func() error { _, err := upgrade(w, api.JailUpgrade{DryRun: true}); return err },
		"base add": func() error { _, err := upgrade(w, api.JailUpgrade{AddPackages: []string{"FreeBSD-ssh"}}); return err },
		"rollback": func() error {
			_, err := w.S.RollbackJail(bg, "alpha_web", RollbackOptions{Stamp: "1"}, false)
			return err
		},
		"snapshot": func() error { _, err := w.S.SnapshotJail(bg, "alpha_web", "s1", ""); return err },
		"copy":     func() error { _, err := w.S.CopyJail(bg, "alpha_web", "alpha_web2", "uuid-2", "1", false); return err },
		"export":   func() error { _, err := w.S.PlanExport(bg, "alpha_web", "", false); return err },
		"base list": func() error {
			_, err := w.S.BasePackages(bg, "alpha_web")
			return err
		},
	}
	for name, op := range ops {
		refusedForLayout(t, name, op())
	}
	if w.state() != before {
		t.Fatalf("a refused operation changed the jail:\n%s", w.state())
	}
	if gone, err := w.S.CleanupRootOld(bg, "alpha_web"); gone || err != nil {
		t.Fatalf("housekeeping touched the jail: %v %v", gone, err)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func asAPI(err error, ae **api.Error) bool { return errors.As(err, ae) }

// A new jail's container says that no jail has run in its root (the helper lets the package scripts of an installation
// from the host run there, an app's); the first start ends that for good.
func TestNeverStartedEndsAtTheFirstStart(t *testing.T) {
	w := newWorld(t)
	if _, err := w.S.CreateJail(bg, w.jail("web"), false); err != nil {
		t.Fatal(err)
	}
	if ok, _ := w.Files.Exists(bg, jroot+"/"+NeverStartedFile); !ok {
		t.Fatal("a new jail is not marked as never started")
	}
	if err := w.S.StartJail(bg, "alpha_web"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := w.Files.Exists(bg, jroot+"/"+NeverStartedFile); ok {
		t.Fatal("a started jail is still marked as never started")
	}
}

// The datasets an interrupted run of JailOps 0.9.2 leaves (root.new, root.stale, a root.old housekeeping will not
// destroy) are named, and nothing is done to them; a root.old with its cleanup mark is housekeeping's and not named.
func TestLeftoversOfJailOps092AreNamed(t *testing.T) {
	w := newWorld(t)
	w.created("web")
	if l, err := w.S.Leftovers(bg, "alpha_web"); err != nil || len(l) != 0 {
		t.Fatal(l, err)
	}
	for _, d := range []string{jc + "/root.new", jc + "/root.old"} {
		if err := w.Z.Create(bg, d, map[string]string{PropManaged: "on"}); err != nil {
			t.Fatal(err)
		}
	}
	l, err := w.S.Leftovers(bg, "alpha_web")
	if err != nil || strings.Join(l, " ") != jc+"/root.new "+jc+"/root.old" {
		t.Fatal(l, err)
	}
	w.Z.Set(bg, jc+"/root.old", map[string]string{PropCleanupAfter: "2026-01-01T00:00:00Z"})
	if l, _ := w.S.Leftovers(bg, "alpha_web"); strings.Join(l, " ") != jc+"/root.new" {
		t.Fatal(l)
	}
}
