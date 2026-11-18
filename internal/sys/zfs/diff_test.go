package zfs

import (
	"context"
	"testing"

	"jailops.net/jailops/internal/sys/run"
)

const diffOut = "M\t/\t/jails/web/root/etc\n" +
	"M\tF\t/jails/web/root/etc/rc.conf\n" +
	"+\tF\t/jails/web/root/usr/local/etc/web.conf\n" +
	"-\t@\t/jails/web/root/usr/old-link\n" +
	"R\tF\t/jails/web/root/home/a.txt\t/jails/web/root/home/b.txt\n"

func TestParseDiffKinds(t *testing.T) {
	r := ParseDiff(diffOut, 100, nil)
	if r.Total != 4 || r.Truncated {
		t.Fatalf("total %d truncated %v", r.Total, r.Truncated)
	}
	want := []DiffEntry{
		{Kind: "M", Type: "F", Path: "/jails/web/root/etc/rc.conf"},
		{Kind: "R", Type: "F", Path: "/jails/web/root/home/a.txt", To: "/jails/web/root/home/b.txt"},
		{Kind: "+", Type: "F", Path: "/jails/web/root/usr/local/etc/web.conf"},
		{Kind: "-", Type: "@", Path: "/jails/web/root/usr/old-link"},
	}
	if len(r.Entries) != len(want) {
		t.Fatalf("entries %v", r.Entries)
	}
	for i := range want {
		if r.Entries[i] != want[i] {
			t.Errorf("entry %d: %+v, want %+v", i, r.Entries[i], want[i])
		}
	}
	if r.Counts["+"] != 1 || r.Counts["-"] != 1 || r.Counts["M"] != 1 || r.Counts["R"] != 1 {
		t.Errorf("counts %v", r.Counts)
	}
}

func TestParseDiffLimit(t *testing.T) {
	r := ParseDiff(diffOut, 2, nil)
	// the first two by path, whatever order zfs printed them in
	if len(r.Entries) != 2 || r.Total != 4 || !r.Truncated || r.Counts["R"] != 1 || r.Entries[0].Kind != "M" || r.Entries[1].Kind != "R" {
		t.Fatalf("%+v", r)
	}
}

func TestParseDiffEscapes(t *testing.T) {
	// a space is \0040, é is \0303\0251, a backslash that starts no escape stays
	out := "+\tF\t/r/my\\0040file\\0303\\0251.txt\n+\tF\t/r/a\\b\n"
	r := ParseDiff(out, 10, nil)
	if len(r.Entries) != 2 || r.Entries[1].Path != "/r/my fileé.txt" || r.Entries[0].Path != `/r/a\b` {
		t.Fatalf("%+v", r.Entries)
	}
}

func TestParseDiffSkipsMountDirs(t *testing.T) {
	out := "+\t/\t/r/usr/local\n+\t/\t/r/home\n+\tF\t/r/home/user.txt\n+\t/\t/r/srv\n"
	r := ParseDiff(out, 10, []string{"/r/usr/local", "/r/home"})
	if r.Total != 2 || r.Counts["+"] != 2 || len(r.Entries) != 2 || r.Entries[0].Path != "/r/home/user.txt" {
		t.Fatalf("%+v", r)
	}
}

func TestParseDiffEmpty(t *testing.T) {
	r := ParseDiff("", 10, nil)
	if r.Total != 0 || r.Entries == nil || len(r.Entries) != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestCommandDiffRunsReadOnly(t *testing.T) {
	f := run.NewFake()
	f.On("zfs", []string{"diff", "-FH", "tank/jailops/images/base/g1@ro", "tank/jailops/jails/web/root"}, diffOut)
	c := NewClient(f)
	ctx := context.Background()
	r, err := c.Diff(ctx, "tank/jailops/images/base/g1@ro", "tank/jailops/jails/web/root", 10, nil)
	if err != nil || r.Total != 4 {
		t.Fatalf("%v %+v", err, r)
	}
	calls := f.Calls()
	if len(calls) != 1 || calls[0].Cmd.Timeout == 0 || calls[0].Cmd.MaxOutput == 0 {
		t.Errorf("the command has no time and size bound: %+v", calls)
	}
	if _, err := c.Diff(ctx, "tank/x", "tank/y", 10, nil); err == nil {
		t.Error("a diff from a dataset, not a snapshot, was accepted")
	}
	if _, err := c.Diff(ctx, "tank/x@ro", "-r", 10, nil); err == nil {
		t.Error("an option as the dataset was accepted")
	}
	if len(f.Calls()) != 1 {
		t.Error("a refused diff ran a command")
	}
}
