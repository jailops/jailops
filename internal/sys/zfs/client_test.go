package zfs

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"

	"jailops.net/jailops/internal/sys/run"
	"jailops.net/jailops/sdk/api"
)

func TestCommandArgv(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name string
		call func(c Client) error
		want string
	}{
		{"create", func(c Client) error {
			return c.Create(ctx, CreateOpts{Name: "tank/a", Parents: true, Props: map[string]string{"jailops:managed": "on", "compression": "lz4"}})
		}, "zfs create -p -o compression=lz4 -o jailops:managed=on tank/a"},
		{"volume", func(c Client) error { return c.Create(ctx, CreateOpts{Name: "tank/v", VolumeSize: "10G"}) }, "zfs create -V 10G tank/v"},
		{"set", func(c Client) error { return c.Set(ctx, "tank/a", map[string]string{"jailops:id": "x"}) }, "zfs set jailops:id=x tank/a"},
		{"clone", func(c Client) error { return c.Clone(ctx, "tank/a@ro", "tank/b", nil, false) }, "zfs clone tank/a@ro tank/b"},
		{"snapshot atomic", func(c Client) error {
			return c.Snapshot(ctx, SnapshotOpts{Names: []string{"tank/a@s", "tank/b@s", "tank/c@s"}})
		}, "zfs snapshot tank/a@s tank/b@s tank/c@s"},
		{"destroy", func(c Client) error { return c.Destroy(ctx, "tank/a@s", DestroyOpts{Defer: true}) }, "zfs destroy -d tank/a@s"},
		{"destroy R", func(c Client) error { return c.Destroy(ctx, "tank/a", DestroyOpts{Recursive: true, Dependents: true}) }, "zfs destroy -r -R tank/a"},
		{"rename", func(c Client) error { return c.Rename(ctx, "tank/a", "tank/b", RenameOpts{Parents: true}) }, "zfs rename -p tank/a tank/b"},
		{"rollback", func(c Client) error { return c.Rollback(ctx, "tank/a@s", RollbackOpts{DestroyNewer: true}) }, "zfs rollback -r tank/a@s"},
		{"hold", func(c Client) error { return c.Hold(ctx, "keep", "tank/a@s", "tank/b@s") }, "zfs hold keep tank/a@s tank/b@s"},
		{"release", func(c Client) error { return c.Release(ctx, "keep", "tank/a@s") }, "zfs release keep tank/a@s"},
		{"bookmark", func(c Client) error { return c.Bookmark(ctx, "tank/a@s", "tank/a#b") }, "zfs bookmark tank/a@s tank/a#b"},
		{"inherit", func(c Client) error { return c.Inherit(ctx, "tank/a", "compression") }, "zfs inherit compression tank/a"},
		{"abort", func(c Client) error { return c.ReceiveAbort(ctx, "tank/a") }, "zfs receive -A tank/a"},
		{"send full", func(c Client) error {
			return c.Send(ctx, SendOpts{Snapshot: "tank/a@s2"}, &bytes.Buffer{})
		}, "zfs send tank/a@s2"},
		{"send incr raw", func(c Client) error {
			return c.Send(ctx, SendOpts{Snapshot: "tank/a@s2", From: "tank/a#b", Raw: true, Compressed: true}, &bytes.Buffer{})
		}, "zfs send -w -c -i tank/a#b tank/a@s2"},
		{"send I", func(c Client) error {
			return c.Send(ctx, SendOpts{Snapshot: "tank/a@s2", From: "tank/a@s1", Intermediary: true}, &bytes.Buffer{})
		}, "zfs send -I tank/a@s1 tank/a@s2"},
		{"send resume", func(c Client) error { return c.Send(ctx, SendOpts{ResumeToken: "1-ab-cd"}, &bytes.Buffer{}) }, "zfs send -t 1-ab-cd"},
		{"receive", func(c Client) error {
			return c.Receive(ctx, ReceiveOpts{Target: "tank/r", Resumable: true}, strings.NewReader("x"))
		}, "zfs receive -u -s tank/r"},
		{"receive force", func(c Client) error {
			return c.Receive(ctx, ReceiveOpts{Target: "tank/r", Force: true, Exclude: []string{"mountpoint"}}, strings.NewReader("x"))
		}, "zfs receive -u -F -x mountpoint tank/r"},
	}
	for _, tc := range tests {
		f := run.NewFake()
		f.Add(run.Rule{Name: "zfs", Args: []string{run.RestArgs}})
		if err := tc.call(NewClient(f)); err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if got := f.Argv(); len(got) != 1 || got[0] != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}

func TestCommandRejectsBadInput(t *testing.T) {
	ctx := context.Background()
	f := run.NewFake()
	c := NewClient(f)
	bad := []error{
		c.Create(ctx, CreateOpts{Name: "-r"}),
		c.Create(ctx, CreateOpts{Name: "tank/a", Props: map[string]string{"x y": "1"}}),
		c.Create(ctx, CreateOpts{Name: "tank/a", Props: map[string]string{"a:b": "1\n2"}}),
		c.Create(ctx, CreateOpts{Name: "tank/a", VolumeSize: "-1"}),
		c.Snapshot(ctx, SnapshotOpts{Names: []string{"tank/a@s", "--x@y"}}),
		c.Snapshot(ctx, SnapshotOpts{}),
		c.Destroy(ctx, "tank/a", DestroyOpts{Defer: true}),
		c.Destroy(ctx, "-r", DestroyOpts{}),
		c.Rename(ctx, "tank/a", "tank/a@s", RenameOpts{}),
		c.Send(ctx, SendOpts{Snapshot: "tank/a"}, &bytes.Buffer{}),
		c.Send(ctx, SendOpts{Snapshot: "tank/a@s", From: "tank/a"}, &bytes.Buffer{}),
		c.Send(ctx, SendOpts{ResumeToken: "-w"}, &bytes.Buffer{}),
		c.Receive(ctx, ReceiveOpts{Target: "tank/a@s"}, nil),
		c.Hold(ctx, "a b", "tank/a@s"),
	}
	for i, err := range bad {
		if !api.IsCode(err, api.CodeInvalid) {
			t.Errorf("case %d: %v", i, err)
		}
	}
	if n := len(f.Calls()); n != 0 {
		t.Errorf("%d commands ran", n)
	}
}

func TestCommandReads(t *testing.T) {
	ctx := context.Background()
	f := run.NewFake()
	f.On("zfs", []string{"list", "-H", "-p", "-t", "filesystem,volume", "-r", "-o", "name,type,jailops:managed", "tank/jailops"},
		"tank/jailops\tfilesystem\ton\ntank/jailops/x\tfilesystem\t-\n")
	f.On("zfs", []string{"get", "-H", "-p", "-o", "property,value,source", "guid", "tank/a@s"}, "guid\t42\t-\n")
	f.On("zfs", []string{"get", "-H", "-p", "-o", "property,value,source", "written@s1", "tank/a"}, "written@s1\t4096\t-\n")
	f.On("zfs", []string{"get", "-H", "-p", "-o", "property,value,source", "clones", "tank/a@s"}, "clones\ttank/c1,tank/c2\t-\n")
	f.On("zfs", []string{"get", "-H", "-p", "-o", "property,value,source", "origin", "tank/c1"}, "origin\ttank/a@s\t-\n")
	f.On("zfs", []string{"get", "-H", "-p", "-o", "property,value,source", "origin", "tank/a"}, "origin\t-\t-\n")
	f.On("zfs", []string{"holds", "-H", "-p", "tank/a@s"}, "tank/a@s\tk\t100\n")
	f.On("zpool", []string{"get", "-H", "-p", "-o", "property,value", "all", "tank"}, "feature@bookmarks\tactive\nfeature@encryption\tenabled\n")
	f.On("zpool", []string{"list", "-H", "-p", "-o", "name,size,allocated,free,health"}, "tank\t10\t4\t6\tONLINE\n")
	c := NewClient(f)

	ds, err := c.List(ctx, ListOpts{Root: "tank/jailops", Recursive: true, Props: []string{"jailops:managed"}})
	if err != nil || len(ds) != 2 || ds[0].Props["jailops:managed"] != "on" {
		t.Fatal(ds, err)
	}
	if g, err := c.GUID(ctx, "tank/a@s"); err != nil || g != 42 {
		t.Fatal(g, err)
	}
	if w, err := c.Written(ctx, "tank/a", "tank/a@s1"); err != nil || w != 4096 {
		t.Fatal(w, err)
	}
	if cl, err := c.Clones(ctx, "tank/a@s"); err != nil || !reflect.DeepEqual(cl, []string{"tank/c1", "tank/c2"}) {
		t.Fatal(cl, err)
	}
	if o, err := c.Origin(ctx, "tank/c1"); err != nil || o != "tank/a@s" {
		t.Fatal(o, err)
	}
	if o, err := c.Origin(ctx, "tank/a"); err != nil || o != "" {
		t.Fatal(o, err)
	}
	if h, err := c.Holds(ctx, "tank/a@s"); err != nil || len(h) != 1 {
		t.Fatal(h, err)
	}
	if ft, err := c.ActiveFeatures(ctx, "tank"); err != nil || !reflect.DeepEqual(ft, []string{"bookmarks"}) {
		t.Fatal(ft, err)
	}
	if ps, err := c.ListPools(ctx); err != nil || len(ps) != 1 || ps[0].Size != 10 {
		t.Fatal(ps, err)
	}
}

func TestSendStreamsToWriter(t *testing.T) {
	f := run.NewFake()
	f.Add(run.Rule{Name: "zfs", Args: []string{"send", "tank/a@s"}, Result: run.Result{Stdout: []byte("STREAM")}})
	var buf bytes.Buffer
	if err := NewClient(f).Send(context.Background(), SendOpts{Snapshot: "tank/a@s"}, &buf); err != nil || buf.String() != "STREAM" {
		t.Fatal(buf.String(), err)
	}
	if f.Calls()[0].Cmd.Timeout != run.NoTimeout {
		t.Error("stream must not have the default timeout")
	}
}

func TestErrorMapping(t *testing.T) {
	tests := []struct {
		stderr string
		code   api.Code
		check  func(error) bool
	}{
		{"cannot open 'tank/x': dataset does not exist", api.CodeNotFound, IsNotFound},
		{"cannot destroy 'tank/a@s': snapshot has dependent clones\nuse '-R' to destroy the following datasets:\ntank/c", api.CodeBusy, IsHasClones},
		{"cannot unmount '/tank/a': pool or dataset is busy", api.CodeBusy, IsBusy},
		{"cannot destroy snapshot tank/a@s: dataset is busy", api.CodeBusy, IsBusy},
		{"cannot destroy 'tank/a@s': it's being held", api.CodeBusy, IsHeld},
		{"cannot create 'tank/a': dataset already exists", api.CodeAlreadyExists, nil},
		{"cannot receive incremental stream: most recent snapshot of tank/r does not match incremental source", api.CodeChainBroken, nil},
		{"cannot create 'tank/a': out of space", api.CodePoolExhausted, nil},
		{"weird failure", api.CodeInternal, nil},
	}
	for _, tc := range tests {
		f := run.NewFake()
		f.OnFail("zfs", []string{run.RestArgs}, 1, tc.stderr)
		err := NewClient(f).Destroy(context.Background(), "tank/a@s", DestroyOpts{})
		if !api.IsCode(err, tc.code) || (tc.check != nil && !tc.check(err)) {
			t.Errorf("%q -> %v", tc.stderr, err)
		}
	}
}
