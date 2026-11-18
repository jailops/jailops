package zfsmodel

import (
	"context"
	"sort"
	"strings"

	"jailops.net/jailops/internal/sys/zfs"
)

// Diff lists the files that differ between a snapshot and a dataset (or a later snapshot), as `zfs diff -FH` does: a
// file with another content is M, one only in the dataset is +, one only in the snapshot is -. The paths are below the
// mount point of the dataset.
func (m *Model) Diff(_ context.Context, from, to string, limit int, skip []string) (zfs.DiffResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, s, err := m.lookupSnap(from)
	if err != nil {
		return zfs.DiffResult{}, err
	}
	var files map[string]string
	var mp string
	if strings.Contains(to, "@") {
		d, t, err := m.lookupSnap(to)
		if err != nil {
			return zfs.DiffResult{}, err
		}
		files, mp = t.files, d.props["mountpoint"]
	} else {
		d, err := m.get(to)
		if err != nil {
			return zfs.DiffResult{}, err
		}
		files, mp = d.files, d.props["mountpoint"]
	}
	var lines []string
	for p, c := range files {
		old, ok := s.files[p]
		switch {
		case !ok:
			lines = append(lines, "+\tF\t"+mp+p)
		case old != c:
			lines = append(lines, "M\tF\t"+mp+p)
		}
	}
	for p := range s.files {
		if _, ok := files[p]; !ok {
			lines = append(lines, "-\tF\t"+mp+p)
		}
	}
	sort.Strings(lines)
	return zfs.ParseDiff(strings.Join(lines, "\n"), limit, skip), nil
}
