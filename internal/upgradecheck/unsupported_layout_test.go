package upgradecheck

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/internal/lifecycle"
	"jailops.net/jailops/internal/sys/zfs"
	"jailops.net/jailops/sdk/api"
)

// oldLayoutJail puts, on the pool and the files of e, a jail named name the way JailOps 0.9.2 and before made one with
// the root layout this release does not support: the container carries the layout property, the root is a clone of the generation fb#g1, and /etc and /var/db are
// datasets of their own mounted over it. Its spec.json is the one the release of the fixture wrote (spec), made to fit
// the test host: its name, the network lan of the host and no volume, policy or exposure the host does not have.
func oldLayoutJail(t *testing.T, e *dtest.Env, name string, spec []byte) {
	t.Helper()
	ctx := context.Background()
	c := "tank/jailops/projects/" + dtest.SeedProject + "/jails/" + name
	mp := "/jailops/projects/" + dtest.SeedProject + "/jails/" + name
	uuid := "99999999-9999-4999-8999-0000000000" + map[string]string{"old": "01", "old2": "02"}[name]
	base := func(kind string, extra map[string]string) map[string]string {
		m := map[string]string{"jailops:managed": "on", "jailops:id": uuid, "jailops:kind": kind, "jailops:name": name, "jailops:project": dtest.SeedProject}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(e.ZFS.Create(ctx, zfs.CreateOpts{Name: c, Props: base("container", map[string]string{"mountpoint": mp, "jailops:layout": lifecycle.OldLayoutValue, "jailops:image": "fb", "jailops:complete": "on"})}))
	must(e.ZFS.Clone(ctx, "tank/jailops/images/fb/g1@ro", c+"/root", base("root", map[string]string{"mountpoint": mp + "/root", "jailops:gen": "1", "jailops:image": "fb"}), false))
	for _, p := range []struct{ ds, path string }{{"etc", "/etc"}, {"var-db", "/var/db"}} {
		must(e.ZFS.Create(ctx, zfs.CreateOpts{Name: c + "/" + p.ds, Props: base("persistent", map[string]string{"jailops:path": p.path, "jailops:class": "data", "mountpoint": mp + "/root" + p.path})}))
	}
	e.Files.Put(mp+"/root/etc/rc.conf", []byte("sshd_enable=YES\n"))
	e.Files.Put(mp+"/root/var/db/app/data", []byte("rows\n"))

	var rec map[string]any
	if err := json.Unmarshal(spec, &rec); err != nil {
		t.Fatal(err)
	}
	if s, _ := rec["spec"].(map[string]any); s["layout"] != lifecycle.OldLayoutValue {
		t.Fatalf("the fixture's jail does not have the old layout: %v", s["layout"])
	}
	meta := rec["metadata"].(map[string]any)
	meta["name"], meta["project"], meta["uuid"] = name, dtest.SeedProject, uuid
	s := rec["spec"].(map[string]any)
	s["network"] = []any{map[string]any{"network": "lan", "ipv4": "10.20.0.4" + map[string]string{"old": "1", "old2": "2"}[name]}}
	for _, k := range []string{"volumes", "firewall", "expose", "proxy", "storage"} {
		delete(s, k)
	}
	b, _ := json.MarshalIndent(rec, "", "  ")
	e.Files.Put(mp+"/spec.json", b)
	e.Files.Put(mp+"/bundle.json", b)
}

// datasetsOf is every dataset below c with every property: what "untouched" is measured with.
func datasetsOf(t *testing.T, e *dtest.Env, c string) []zfs.Dataset {
	t.Helper()
	rows, err := e.ZFS.List(context.Background(), zfs.ListOpts{Root: c, Recursive: true, Types: []string{"filesystem", "snapshot"},
		Props: []string{"origin", "mountpoint", "jailops:kind", "jailops:layout", "jailops:id", "jailops:gen", "jailops:image", "jailops:path", "jailops:complete"}})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// A host upgraded to this release with jails of the old root layout on it: the daemon starts, finds them, never touches
// their data, marks them (condition UnsupportedLayout, doctor jail.unsupported_layout FAIL), lets them be stopped and
// deleted, and refuses everything else, a start too, with the fix. The jail records are those of the newest fixture
// of a release that still had the layout (0.9.2 and before).
func TestADaemonOnAHostWithUnsupportedLayoutJails(t *testing.T) {
	vs := versions(t)
	var spec []byte
	for i := len(vs) - 1; i >= 0 && spec == nil; i-- {
		b, err := os.ReadFile(filepath.Join(fixtureRoot, vs[i], "jails", "shop", "web", "spec.json"))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(b, []byte(`"layout"`)) {
			spec = b
		}
	}
	if spec == nil {
		t.Fatal("no fixture has a jail with the layout field")
	}
	e := dtest.New(t, dtest.Options{})
	e.Seed()
	oldLayoutJail(t, e, "old", spec)
	const c = "tank/jailops/projects/" + dtest.SeedProject + "/jails/old"
	before := datasetsOf(t, e, c)
	e.Stop()

	// the upgraded daemon starts on the same pool
	e2 := dtest.New(t, dtest.Options{Reuse: e})
	ctx := context.Background()
	j, err := e2.Admin.Jails().Get(ctx, "old")
	if err != nil {
		t.Fatalf("the jail is not found: %v\n%s", err, e2.Log())
	}
	var cond *api.Condition
	for i := range j.Status.Conditions {
		if j.Status.Conditions[i].Type == api.ConditionUnsupportedLayout {
			cond = &j.Status.Conditions[i]
		}
	}
	if cond == nil || cond.Status != api.ConditionTrue || cond.Reason != "EarlierRelease" || !strings.Contains(cond.Message, "jailops jail convert old --layout thick") || !strings.Contains(cond.Message, "create a new jail and move its data over") {
		t.Fatalf("condition: %+v (all: %+v)", cond, j.Status.Conditions)
	}
	t.Logf("condition: %s", cond.Message)
	rep, err := e2.Admin.Doctor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ck := range rep.Checks {
		if ck.Name == "jail.unsupported_layout" && ck.Status == api.DoctorFail && strings.Contains(ck.Message, dtest.SeedProject+"/old") && strings.Contains(ck.Hint, "jailops jail convert old") {
			found = true
		}
	}
	if !found {
		t.Fatalf("doctor has no jail.unsupported_layout FAIL for the jail: %+v", rep.Checks)
	}

	// every operation but stop and delete is refused, with the fix, and changes nothing
	refused := func(what string, err error) {
		t.Helper()
		var ae *api.Error
		if !api.IsCode(err, api.CodeUnsupported) || !strings.Contains(err.Error(), "root layout this release does not support") || !asErr(err, &ae) || !strings.Contains(ae.Hint, "jailops jail convert old --layout thick") {
			t.Errorf("%s: %v", what, err)
		}
	}
	_, err = e2.Admin.Jails().Start(ctx, "old")
	refused("start", err)
	_, err = e2.Admin.Jails().Restart(ctx, "old")
	refused("restart", err)
	_, err = e2.Admin.Jails().Upgrade(ctx, "old", api.JailUpgrade{})
	refused("upgrade", err)
	_, err = e2.Admin.Jails().Rollback(ctx, "old", api.JailRollback{})
	refused("rollback", err)
	_, err = e2.Admin.Jails().Copy(ctx, "old", api.JailCopy{Name: "copy"})
	refused("copy", err)
	if op, err := e2.Admin.Jails().Snapshots("old").Create(ctx, "s1", ""); err == nil {
		_, err = op.Wait(ctx)
		refused("snapshot", err)
	} else {
		refused("snapshot", err)
	}
	// the one guard of the HTTP layer refuses every other change of the jail
	tok := e2.Token(api.PermAdmin)
	q := "?project=" + dtest.SeedProject
	for _, rq := range []struct{ method, path, body string }{
		{"PATCH", "/api/v1/jails/old" + q, `{"spec":{"hostname":"x"}}`},
		{"POST", "/api/v1/jails/old/config" + q, `{"set":{"env.A":"b"}}`},
		{"DELETE", "/api/v1/jails/old/snapshots/s1" + q, ""},
		{"POST", "/api/v1/jails/old/exec" + q, `{"argv":["true"]}`},
		{"POST", "/api/v1/jails/old/rename" + q, `{"new_name":"older"}`},
		{"POST", "/api/v1/jails/old/apply" + q, `{"recipe":{"steps":[{"run":["true"]}]}}`},
		{"PUT", "/api/v1/jails/old/files/etc/x" + q, "x"},
	} {
		code, body := e2.Raw(rq.method, rq.path, tok, rq.body)
		if code != 422 && code != 501 || !strings.Contains(body, "root layout this release does not support") {
			t.Errorf("%s %s: %d %s", rq.method, rq.path, code, body)
		}
	}
	if after := datasetsOf(t, e2, c); !reflect.DeepEqual(before, after) {
		t.Fatalf("the jail's datasets changed:\nbefore %+v\nafter  %+v", before, after)
	}

	// it is stopped (a stopped one stays stopped); its data stays
	if op, err := e2.Admin.Jails().Stop(ctx, "old"); err != nil {
		t.Fatal(err)
	} else if _, err := op.Wait(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if e2.HasFile("/jailops/projects/" + dtest.SeedProject + "/jails/old/root/etc/hosts") {
		t.Error("JailOps wrote /etc/hosts into the jail")
	}
	if got := e2.File("/jailops/projects/" + dtest.SeedProject + "/jails/old/root/var/db/app/data"); got != "rows\n" {
		t.Fatalf("the data of the jail: %q", got)
	}
	if o, _ := e2.ZFS.Origin(ctx, c+"/root"); o != "tank/jailops/images/fb/g1@ro" {
		t.Fatalf("the root of the jail changed: origin %q", o)
	}

	// and it is deleted like any jail
	op, err := e2.Admin.Jails().Delete(ctx, "old")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := op.Wait(ctx); err != nil {
		t.Fatalf("delete: %v\n%s", err, e2.Log())
	}
	if ok, _ := e2.ZFS.Exists(ctx, c); ok {
		t.Fatal("the jail was not deleted")
	}
	if rep, _ := e2.Admin.Doctor(ctx); rep.Checks != nil {
		for _, ck := range rep.Checks {
			if ck.Name == "jail.unsupported_layout" && ck.Status != api.DoctorOK {
				t.Fatalf("the check still fails without such a jail: %+v", ck)
			}
		}
	}
}

func asErr(err error, ae **api.Error) bool { return errors.As(err, ae) }
