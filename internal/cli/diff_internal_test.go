package cli

import (
	"bytes"
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
)

// The text of a diff: the four kinds, a rename with both paths, a line that was cut, a path with a space and unicode, a
// path that would drive a terminal, a dataset without changes and the one from a tree comparison.
func TestPrintDiff(t *testing.T) {
	var out bytes.Buffer
	a := &app{env: &Env{Stdout: &out}}
	r := api.JailDiff{Jail: "web", Image: "freebsd-15-1#g3", Datasets: []api.DiffDataset{
		{Name: "root", Path: "/", Base: api.DiffBaseImage, Total: 5, Truncated: true,
			Counts: map[string]int{"added": 2, "modified": 1, "removed": 1, "renamed": 1},
			Entries: []api.DiffEntry{
				{Kind: api.DiffAdded, Path: "/opt/my file é.txt"},
				{Kind: api.DiffModified, Path: "/boot/loader.conf"},
				{Kind: api.DiffRemoved, Path: "/bin/old"},
				{Kind: api.DiffRenamed, Path: "/srv/a", To: "/srv/b"},
			}},
		{Name: "etc", Path: "/etc", Base: api.DiffBaseBaseline, Counts: map[string]int{}, Entries: []api.DiffEntry{}},
		{Name: "home", Path: "/home", Base: api.DiffBaseTree, Note: "compared with the image, not with a snapshot", Total: 1,
			Counts: map[string]int{"added": 1}, Entries: []api.DiffEntry{{Kind: api.DiffAdded, Path: "/home/\x1b[31mred"}}},
	}}
	a.printDiff(r, false)
	got := out.String()
	if strings.Contains(got, "\x1b") {
		t.Errorf("a control sequence of a path reached the terminal: %q", got)
	}
	for _, line := range []string{
		"root /: since freebsd-15-1#g3\n", "  + /opt/my file é.txt\n", "  R /srv/a -> /srv/b\n",
		"  ... and 1 more (--limit 5 lists them all)\n", "home /home: compared with the image, not with a snapshot\n",
		"total: 6 changes (3 added, 1 modified, 1 removed, 1 renamed)\n",
	} {
		if !strings.Contains(got, line) {
			t.Errorf("no %q in\n%s", line, got)
		}
	}
	if strings.Contains(got, "etc /etc") {
		t.Errorf("a dataset without changes has a heading:\n%s", got)
	}
	out.Reset()
	a.printDiff(r, true)
	if !strings.Contains(out.String(), "etc   /etc   no changes\n") || !strings.Contains(out.String(), "root  /      5 changes (") {
		t.Errorf("summary:\n%s", out.String())
	}
}
