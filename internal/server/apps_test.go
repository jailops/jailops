package server_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"jailops.net/jailops/internal/daemon"
	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/sdk/api"
)

const appDemo = `version: 1
name: demo
title: Demo
summary: A demo application.
category: web
homepage: https://example.org/
image: {os: freebsd, release_min: "15.1"}
packages:
  - {name: demo-server, version: "1.0"}
services:
  - {name: demo}
ports:
  - {name: http, port: 8080}
data:
  - {path: /var/db/demo}
health: {tcp: 8080}
`

// The catalog is read from the configured directory on each request: a valid
// definition is served with the hash of its file, an invalid one is listed
// with the reason and refused by GET, and any authenticated identity may read.
func TestAppsCatalog(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "demo.yaml"), []byte(appDemo), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.yaml"), []byte("version: 1\nname: broken\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := dtest.New(t, dtest.Options{Mutate: func(c *daemon.Config) { c.Apps.CatalogDir = dir }})
	e.Seed()
	c := ctxT(t)
	info, err := e.Admin.Info(c)
	if err != nil || !slices.Contains(info.APIExtensions, "apps") {
		t.Fatalf("%v %v", err, info.APIExtensions)
	}
	list, err := e.Admin.Apps().List(c)
	if err != nil || len(list) != 2 {
		t.Fatalf("%v %+v", err, list)
	}
	if list[0].Name != "broken" || list[0].Valid || !strings.Contains(list[0].Error, "title") || list[0].Title != "" {
		t.Errorf("%+v", list[0])
	}
	if list[1].Name != "demo" || !list[1].Valid || list[1].Title != "Demo" || list[1].Category != api.AppWeb || list[1].Version != "1.0" {
		t.Errorf("%+v", list[1])
	}
	det, err := e.Admin.Apps().Get(c, "demo")
	if err != nil {
		t.Fatal(err)
	}
	d := det.Definition
	if d.Name != "demo" || d.Packages[0].Repo != "ports" || d.Ports[0].Proto != "tcp" || d.Health.WaitSeconds != 60 || det.File != "demo.yaml" ||
		!strings.HasPrefix(det.Hash, "sha256:") || det.Icon != "" {
		t.Fatalf("%+v", det)
	}
	_, err = e.Admin.Apps().Get(c, "broken")
	wantCode(t, err, api.CodeInvalid)
	_, err = e.Admin.Apps().Get(c, "nothing")
	wantCode(t, err, api.CodeNotFound)
	_, err = e.Admin.Apps().Get(c, "../demo")
	wantCode(t, err, api.CodeNotFound)
	// A project-restricted readonly identity reads the catalog too.
	ro := e.Scoped(api.PermReadonly, "alpha")
	if l, err := ro.Apps().List(c); err != nil || len(l) != 2 {
		t.Fatalf("%v %+v", err, l)
	}
	if _, err := ro.Apps().Get(c, "demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Anonymous().Apps().List(c); !api.IsCode(err, api.CodeUnauthorized) {
		t.Fatalf("anonymous: %v", err)
	}
	// A definition added later is seen on the next request; a removed one disappears.
	if err := os.WriteFile(filepath.Join(dir, "later.yaml"), []byte(strings.ReplaceAll(appDemo, "name: demo", "name: later")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "broken.yaml")); err != nil {
		t.Fatal(err)
	}
	list, err = e.Admin.Apps().List(c)
	if err != nil || len(list) != 2 || list[0].Name != "demo" || list[1].Name != "later" {
		t.Fatalf("%v %+v", err, list)
	}
}

// Without a catalog directory the catalog is empty, not an error.
func TestAppsCatalogMissingDirectory(t *testing.T) {
	e := dtest.New(t, dtest.Options{Mutate: func(c *daemon.Config) { c.Apps.CatalogDir = filepath.Join(t.TempDir(), "none") }})
	list, err := e.Admin.Apps().List(ctxT(t))
	if err != nil || len(list) != 0 {
		t.Fatalf("%v %+v", err, list)
	}
	_, err = e.Admin.Apps().Get(ctxT(t), "demo")
	wantCode(t, err, api.CodeNotFound)
}
