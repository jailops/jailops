package server_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/internal/jailxfer"
	"jailops.net/jailops/internal/sys/zfs"
	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

func xportEnv(t *testing.T) (*dtest.Env, context.Context) {
	t.Helper()
	e := dtest.New(t, dtest.Options{})
	e.Seed()
	ctx := context.Background()
	op, err := e.Admin.Jails().Create(ctx, dtest.WebJail("web"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := op.Wait(ctx); err != nil {
		t.Fatalf("%v\n%s", err, e.Log())
	}
	return e, ctx
}

func exportOf(t *testing.T, c *sdk.Client, ctx context.Context, name string, o api.JailExportRequest) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := c.Jails().Export(ctx, name, o, &b); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// A jail leaves as one file and comes back as another jail of the same host and project: new name and uuid, its own
// datasets, the same specification.
func TestJailExportImportRoundTrip(t *testing.T) {
	e, ctx := xportEnv(t)
	file := exportOf(t, e.Admin, ctx, "web", api.JailExportRequest{Zstd: true})
	xr, err := jailxfer.NewReader(bytes.NewReader(file))
	if err != nil {
		t.Fatal(err)
	}
	m := xr.Manifest()
	if m.Name != "web" || m.Project != dtest.SeedProject || m.Image.Name != "fb" || m.Layout != api.LayoutThin {
		t.Fatalf("manifest: %+v", m)
	}
	req := api.JailImportRequest{As: "web2"}
	pf, err := e.Admin.Jails().ImportPreflight(ctx, api.JailImportPreflight{Manifest: m, Request: req})
	if err != nil || !pf.OK {
		t.Fatalf("preflight %+v: %v", pf, err)
	}
	res, err := e.Admin.Jails().Import(ctx, req, bytes.NewReader(file))
	if err != nil {
		t.Fatalf("%v\n%s", err, e.Log())
	}
	if res.Jail.Metadata.Name != "web2" || res.Jail.Metadata.UUID == "" {
		t.Fatalf("%+v", res.Jail.Metadata)
	}
	orig, _ := e.Admin.Jails().Get(ctx, "web")
	if res.Jail.Metadata.UUID == orig.Metadata.UUID {
		t.Error("the copy has the uuid of the original")
	}
	if res.Jail.Spec.Image != orig.Spec.Image || res.Jail.Spec.Storage != orig.Spec.Storage {
		t.Errorf("spec: %+v", res.Jail.Spec)
	}
	if res.Jail.Spec.Boot.Autostart {
		t.Error("an imported jail starts at boot")
	}
	if len(res.Jail.Spec.Network) != 1 || res.Jail.Spec.Network[0].IPv4 == orig.Spec.Network[0].IPv4 && orig.Spec.Network[0].IPv4 != "auto" {
		t.Errorf("addresses: %+v", res.Jail.Spec.Network)
	}
	if _, err := e.Admin.Jails().Get(ctx, "web"); err != nil {
		t.Errorf("the original is gone: %v", err)
	}
	noStagedImports(t, e)
}

// The file of an import is kept on the host while it is read and is gone when the import has ended, whether it made a jail or not.
func noStagedImports(t *testing.T, e *dtest.Env) {
	t.Helper()
	ents, _ := os.ReadDir(filepath.Join(e.Cfg.StateDir, "imports"))
	if len(ents) != 0 {
		t.Errorf("%d staged import files are left", len(ents))
	}
}

// The same name is refused with the way out, and nothing is made.
func TestJailImportNameCollision(t *testing.T) {
	e, ctx := xportEnv(t)
	file := exportOf(t, e.Admin, ctx, "web", api.JailExportRequest{})
	_, err := e.Admin.Jails().Import(ctx, api.JailImportRequest{}, bytes.NewReader(file))
	if err == nil || !strings.Contains(err.Error(), "web") {
		t.Fatalf("%v", err)
	}
	n := 0
	for _, err := range e.Admin.Jails().List(ctx) {
		if err != nil {
			t.Fatal(err)
		}
		n++
	}
	if n != 1 {
		t.Errorf("%d jails", n)
	}
}

// A thin jail needs its image on the importing host: the preflight and the import both refuse it, and say which.
func TestJailImportThinNeedsTheImage(t *testing.T) {
	e, ctx := xportEnv(t)
	file := exportOf(t, e.Admin, ctx, "web", api.JailExportRequest{})
	m, err := sdk.ReadExportManifest(bytes.NewReader(file))
	if err != nil {
		t.Fatal(err)
	}
	m.Image.Name = "elsewhere"
	m.Bundle.Spec.Image = "elsewhere"
	pf, err := e.Admin.Jails().ImportPreflight(ctx, api.JailImportPreflight{Manifest: m, Request: api.JailImportRequest{As: "web2"}})
	if err != nil {
		t.Fatal(err)
	}
	if pf.OK || !strings.Contains(strings.Join(append(pf.Errors, pf.Missing...), " "), "elsewhere") {
		t.Errorf("%+v", pf)
	}
}

// A file that is cut off or damaged leaves no jail and no dataset behind.
func TestJailImportDamagedFileLeavesNothing(t *testing.T) {
	e, ctx := xportEnv(t)
	file := exportOf(t, e.Admin, ctx, "web", api.JailExportRequest{})
	before, _ := e.ZFS.List(ctx, listAll)
	for name, bad := range map[string][]byte{
		"truncated": file[:len(file)-20],
		"flipped":   flip(file, len(file)-60),
	} {
		_, err := e.Admin.Jails().Import(ctx, api.JailImportRequest{As: "web2"}, bytes.NewReader(bad))
		if err == nil {
			t.Fatalf("%s: imported", name)
		}
		// the caller is told what is wrong with the file; a damaged file is not an internal error
		want := map[string]string{"truncated": "truncated", "flipped": "corrupt"}[name]
		if !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "internal error") {
			t.Errorf("%s: the error says %q, want it to name the file as %s", name, err.Error(), want)
		}
		if _, gerr := e.Admin.Jails().Get(ctx, "web2"); gerr == nil {
			t.Errorf("%s: a jail was made", name)
		}
		after, _ := e.ZFS.List(ctx, listAll)
		if len(after) != len(before) {
			t.Errorf("%s: datasets %d -> %d", name, len(before), len(after))
		}
		noStagedImports(t, e)
	}
}

func flip(b []byte, at int) []byte {
	c := append([]byte(nil), b...)
	c[at] ^= 0xff
	return c
}

// Export needs the operator role, import the admin role, and both are audited.
func TestJailExportImportPermissions(t *testing.T) {
	e, ctx := xportEnv(t)
	viewer := e.Scoped(api.PermReadonly, dtest.SeedProject)
	if err := viewer.Jails().Export(ctx, "web", api.JailExportRequest{}, io.Discard); err == nil {
		t.Error("a viewer exported a jail")
	}
	oper := e.Scoped(api.PermOperator, dtest.SeedProject)
	file := exportOf(t, oper, ctx, "web", api.JailExportRequest{})
	if _, err := oper.Jails().Import(ctx, api.JailImportRequest{As: "web2"}, bytes.NewReader(file)); err == nil {
		t.Error("an operator imported a jail")
	}
	ev, err := e.Admin.Audit(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	var seen bool
	for _, r := range ev {
		if strings.Contains(r.Action, "/export") && r.Result == "ok" {
			seen = true
		}
	}
	if !seen {
		t.Errorf("the export is not in the audit: %+v", ev)
	}
}

// A file that is not an export file is told apart before anything else.
func TestJailImportNotAnExportFile(t *testing.T) {
	e, ctx := xportEnv(t)
	_, err := e.Admin.Jails().Import(ctx, api.JailImportRequest{As: "x"}, strings.NewReader("hello"))
	if err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("%v", err)
	}
}

var listAll = zfs.ListOpts{Recursive: true, Types: []string{"filesystem", "snapshot"}}

// The file is made into a jail of another project, with the same name, as the file carries no project.
func TestJailImportIntoAnotherProject(t *testing.T) {
	e, ctx := xportEnv(t)
	net := e.MakeNetwork("beta")
	if _, err := e.Admin.Projects().Create(ctx, api.Project{Metadata: api.ObjectMeta{Name: "beta"}, Spec: api.ProjectSpec{
		Networks: []api.ProjectNetwork{{Network: net}}, Storage: []api.ProjectStorage{{Storage: "default"}}}}); err != nil {
		t.Fatal(err)
	}
	file := exportOf(t, e.Admin, ctx, "web", api.JailExportRequest{})
	beta := e.Admin.Project("beta")
	// the network of the file is not the project's: the import says so, and the map fixes it
	if _, err := beta.Jails().Import(ctx, api.JailImportRequest{}, bytes.NewReader(file)); err == nil || !strings.Contains(err.Error(), "lan") {
		t.Fatalf("without a map: %v", err)
	}
	res, err := beta.Jails().Import(ctx, api.JailImportRequest{NetworkMap: map[string]string{"lan": net}}, bytes.NewReader(file))
	if err != nil {
		t.Fatalf("%v\n%s", err, e.Log())
	}
	if res.Jail.Metadata.Name != "web" {
		t.Fatalf("%+v", res.Jail.Metadata)
	}
	if _, err := beta.Jails().Get(ctx, "web"); err != nil {
		t.Error(err)
	}
	if _, err := e.Admin.Jails().Get(ctx, "web"); err != nil {
		t.Error(err)
	}
}
