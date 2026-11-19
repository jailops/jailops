package service_test

import (
	"encoding/json"
	"strings"
	"testing"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/sdk/api"
)

const siteDir = "/srv/data/site"

func TestPlainHostDirectoryAsVolumeIsRefusedWithReasonAndWay(t *testing.T) {
	_, s := newEnv(t)
	ctx := as(globalAdmin, dtest.SeedProject)
	op, err := s.CreateVolume(ctx, api.VolumeCreate{Metadata: api.ObjectMeta{Name: "box"}, Spec: api.VolumeSpec{Path: boxDir}})
	failedWith(t, s, op, err, api.CodeInvalid)
	got := wait(t, s, op)
	if !strings.Contains(got.Error.Message, "plain directory, not a mount") || !strings.Contains(got.Error.Hint, "volume create NAME --from-dir "+boxDir) {
		t.Fatalf("the refusal does not say why or what to do: %+v", got.Error)
	}
}

func TestVolumeFromHostDirectoryCopiesOnce(t *testing.T) {
	e, s := newEnv(t)
	ctx := as(globalAdmin, dtest.SeedProject)
	e.Files.HostTrees = map[string]map[string]string{siteDir: {"index.html": "<h1>hi</h1>", "css/site.css": "body{}"}}
	op, err := s.CreateVolume(ctx, api.VolumeCreate{Metadata: api.ObjectMeta{Name: "site"}, Spec: api.VolumeSpec{UID: 80, GID: 80}, FromDir: siteDir})
	var got api.Volume
	if err := json.Unmarshal(mustOp(t, s, op, err).Metadata, &got); err != nil {
		t.Fatal(err)
	}
	if i := got.Status.Imported; i == nil || i.From != siteDir || i.Files != 2 || i.Bytes != int64(len("<h1>hi</h1>")+len("body{}")) {
		t.Fatalf("%+v", got.Status.Imported)
	}
	if len(e.Files.Imports) != 1 {
		t.Fatalf("imports %v", e.Files.Imports)
	}
	v, err := s.GetVolume(ctx, "site")
	if err != nil || v.Spec.Path != "" || v.Status.Dataset == "" {
		t.Fatalf("%+v %v", v, err)
	}
}

func TestVolumeFromHostDirectoryRefusals(t *testing.T) {
	e, s := newEnv(t)
	ctx := as(globalAdmin, dtest.SeedProject)
	e.Files.HostTrees = map[string]map[string]string{siteDir: {"a": "b"}}
	mk := func(name string, spec api.VolumeSpec, dir, pass string) api.VolumeCreate {
		return api.VolumeCreate{Metadata: api.ObjectMeta{Name: name}, Spec: spec, FromDir: dir, Passphrase: pass}
	}
	// only a global administrator
	_, err := s.CreateVolume(as(alphaOnly, dtest.SeedProject), mk("x", api.VolumeSpec{}, siteDir, ""))
	wantCode(t, err, api.CodeForbidden)
	for name, in := range map[string]api.VolumeCreate{
		"relative":   mk("x", api.VolumeSpec{}, "srv/data", ""),
		"dots":       mk("x", api.VolumeSpec{}, "/srv/data/../etc", ""),
		"trailing /": mk("x", api.VolumeSpec{}, siteDir+"/", ""),
		"with path":  mk("x", api.VolumeSpec{Path: boxDir}, siteDir, ""),
		"encrypted":  mk("x", api.VolumeSpec{Encrypted: true}, siteDir, encPass),
	} {
		if _, err := s.CreateVolume(ctx, in); !api.IsCode(err, api.CodeInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// a directory the helper does not approve: refused before any dataset exists, and the reason names priv.conf
	op, err := s.CreateVolume(ctx, mk("x", api.VolumeSpec{}, "/etc", ""))
	failedWith(t, s, op, err, api.CodeForbidden)
	if h := wait(t, s, op).Error.Hint; !strings.Contains(h, "admin-host-mount") {
		t.Fatalf("hint %q", h)
	}
	if _, err := s.GetVolume(ctx, "x"); !api.IsCode(err, api.CodeNotFound) {
		t.Fatalf("a volume was left behind: %v", err)
	}
	if len(e.Files.Imports) != 0 {
		t.Fatalf("imports %v", e.Files.Imports)
	}
}

func TestVolumeFromHostDirectoryFailingCopyRemovesTheVolume(t *testing.T) {
	e, s := newEnv(t)
	ctx := as(globalAdmin, dtest.SeedProject)
	e.Files.HostTrees = map[string]map[string]string{siteDir: {"a": "b"}}
	e.Files.ImportErr = api.Errorf(api.CodeInternal, "disk full")
	op, err := s.CreateVolume(ctx, api.VolumeCreate{Metadata: api.ObjectMeta{Name: "site"}, FromDir: siteDir})
	failedWith(t, s, op, err, api.CodeInternal)
	if _, err := s.GetVolume(ctx, "site"); !api.IsCode(err, api.CodeNotFound) {
		t.Fatalf("the half-filled volume stayed: %v", err)
	}
}
