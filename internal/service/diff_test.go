package service

import (
	"testing"

	"jailops.net/jailops/internal/lifecycle"
	"jailops.net/jailops/internal/sys/zfs"
	"jailops.net/jailops/sdk/api"
)

func TestDiffDatasetNamesTheKinds(t *testing.T) {
	d := diffDataset(lifecycle.DatasetDiff{Name: "etc", Path: "/etc", Base: lifecycle.DiffBaseBaseline, DiffSet: zfs.DiffResult{
		Entries: []zfs.DiffEntry{
			{Kind: "+", Path: "/etc/new"}, {Kind: "M", Path: "/etc/rc.conf"}, {Kind: "-", Path: "/etc/old"},
			{Kind: "R", Path: "/etc/a", To: "/etc/b"}, {Kind: "?", Path: "/etc/odd"},
		},
		Total: 9, Truncated: true, Counts: map[string]int{"+": 4, "M": 2, "-": 1, "R": 2},
	}})
	want := []api.DiffEntry{
		{Kind: api.DiffAdded, Path: "/etc/new"}, {Kind: api.DiffModified, Path: "/etc/rc.conf"}, {Kind: api.DiffRemoved, Path: "/etc/old"},
		{Kind: api.DiffRenamed, Path: "/etc/a", To: "/etc/b"},
	}
	if len(d.Entries) != len(want) {
		t.Fatalf("%+v", d.Entries)
	}
	for i := range want {
		if d.Entries[i] != want[i] {
			t.Errorf("entry %d: %+v", i, d.Entries[i])
		}
	}
	if d.Total != 9 || !d.Truncated || d.Base != api.DiffBaseBaseline || d.Counts[api.DiffAdded] != 4 || d.Counts[api.DiffRenamed] != 2 {
		t.Errorf("%+v", d)
	}
	if lifecycle.DiffBaseBaseline != api.DiffBaseBaseline || lifecycle.DiffBaseImage != api.DiffBaseImage || lifecycle.DiffBaseTree != api.DiffBaseTree {
		t.Error("the lifecycle and the API name the bases differently")
	}
}
