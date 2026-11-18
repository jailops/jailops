package priv

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"jailops.net/jailops/sdk/api"
)

func TestZFSDiffIsConfined(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	zd := e.local.ZFS()
	for _, c := range []struct{ from, to string }{
		{"tank/other/g1@ro", "tank/jailops/jails/web/root"},      // the snapshot is outside the storage root
		{"tank/jailops/images/b/g1@ro", "tank/other/web/root"},   // so is the dataset
		{"tank/jailops/images/b/g1", "tank/jailops/jails/web"},   // not a snapshot
		{"tank/jailops/images/b/g1@ro", "-r"},                    // an option
		{"tank/jailops/images/b/g1@ro", "tank/jailops/../other"}, // a way out
	} {
		if _, err := zd.Diff(ctx, c.from, c.to, 10, nil); err == nil {
			t.Errorf("diff %s %s was allowed", c.from, c.to)
		}
	}
	e.zfs.SetDiff("tank/jailops/images/b/g1@ro", "tank/jailops/jails/web/root", "+\tF\t/x/y\n")
	r, err := zd.Diff(ctx, "tank/jailops/images/b/g1@ro", "tank/jailops/jails/web/root", 10, nil)
	if err != nil || r.Total != 1 {
		t.Fatalf("%v %+v", err, r)
	}
}

func TestTreeDiff(t *testing.T) {
	te := newTreeEnv(t)
	ctx := context.Background()
	old := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	put := func(root, rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	for _, rel := range []string{"etc/same.conf", "etc/changed.conf", "etc/gone.conf", "bin/tool"} {
		put(te.src, rel, "image")
		put(te.dst, rel, "image")
	}
	put(te.dst, "etc/changed.conf", "edited by hand") // another size
	put(te.dst, "etc/new file é.conf", "new")
	put(te.dst, "share/new/deep.txt", "new")
	if err := os.Remove(filepath.Join(te.dst, "etc/gone.conf")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/same.conf", filepath.Join(te.dst, "etc/link")); err != nil {
		t.Fatal(err)
	}
	r, err := te.local.TreeDiff(ctx, TreeDiffReq{Dir: te.dst, Base: te.src, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, en := range r.Entries {
		rel, _ := filepath.Rel(te.dst, en.Path)
		got[rel] = en.Kind
	}
	want := map[string]string{"etc/changed.conf": "M", "etc/gone.conf": "-", "etc/new file é.conf": "+", "etc/link": "+", "share": "+", "share/new": "+", "share/new/deep.txt": "+"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %q, want %q (all: %v)", k, got[k], v, got)
		}
	}
	if r.Total != 7 || r.Counts["+"] != 5 || r.Counts["-"] != 1 || r.Counts["M"] != 1 {
		t.Errorf("counts %+v", r)
	}
	r, _ = te.local.TreeDiff(ctx, TreeDiffReq{Dir: te.dst, Base: te.src, Limit: 2})
	if len(r.Entries) != 2 || !r.Truncated || r.Total != 7 {
		t.Errorf("limit: %+v", r)
	}
	// a directory the image does not have: all of it is new
	r, err = te.local.TreeDiff(ctx, TreeDiffReq{Dir: te.dst, Base: filepath.Join(te.base, "jailops", "images", "none"), Limit: 100})
	if err != nil || r.Counts["+"] == 0 {
		t.Errorf("missing base: %v %+v", err, r)
	}
}

func TestTreeDiffIsConfined(t *testing.T) {
	te := newTreeEnv(t)
	for _, q := range []TreeDiffReq{
		{Dir: "/etc", Base: te.src},
		{Dir: te.dst, Base: "/etc"},
		{Dir: "etc", Base: te.src},
		{Dir: te.dst + "/../../../etc", Base: te.src},
	} {
		if _, err := te.local.TreeDiff(context.Background(), q); !api.IsCode(err, api.CodeForbidden) {
			t.Errorf("%+v: %v", q, err)
		}
	}
}

func TestZFSDiffSkipIsChecked(t *testing.T) {
	e := newEnv(t)
	zd := e.local.ZFS()
	from, to := "tank/jailops/images/b/g1@ro", "tank/jailops/jails/web/root"
	for _, skip := range [][]string{{"relative"}, {"/a\nb"}, make([]string, 65)} {
		if _, err := zd.Diff(context.Background(), from, to, 10, skip); !api.IsCode(err, api.CodeInvalid) {
			t.Errorf("%v: %v", skip, err)
		}
	}
	if _, err := zd.Diff(context.Background(), from, to, 10, []string{"/jailops/jails/web/root/usr/local"}); err != nil {
		t.Error(err)
	}
}
