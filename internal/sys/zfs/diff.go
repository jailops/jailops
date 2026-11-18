package zfs

import (
	"context"
	"slices"
	"sort"
	"strings"
	"time"

	"jailops.net/jailops/internal/sys/run"
	"jailops.net/jailops/sdk/api"
)

// DiffEntry is one change reported by `zfs diff -FH`: Kind is "+" (added), "-" (removed), "M" (modified) or "R"
// (renamed to To); Type is the one-letter file type of zfs diff ("F" file, "/" directory, "@" symbolic link, and so
// on); Path is the decoded path as zfs prints it, under the mount point of the dataset that was compared.
type DiffEntry struct {
	Kind string `json:"kind"`
	Type string `json:"type"`
	Path string `json:"path"`
	To   string `json:"to,omitempty"`
}

// DiffResult is the answer of a diff: the first Limit entries, how many there are in all and whether the list was cut.
type DiffResult struct {
	Entries   []DiffEntry `json:"entries"`
	Total     int         `json:"total"`
	Truncated bool        `json:"truncated,omitempty"`
	// Counts is how many entries of each kind there are in all (the keys are "+", "-", "M" and "R"), also the ones that
	// were cut from Entries.
	Counts map[string]int `json:"counts,omitempty"`
}

// diffKeep is the most entries a result holds, whatever limit the caller asks for: the count stays exact, the memory
// does not grow with the size of the change.
const diffKeep = api.MaxDiffLimit

// diffTimeout bounds one `zfs diff`; a diff of a big dataset reads both sides.
const diffTimeout = 2 * time.Minute

// ParseDiff reads the output of `zfs diff -FH`. A modified directory is left out (its time changes whenever
// something below it does and it says nothing the files below do not), and so is every entry whose path is in skip (the
// mount points JailOps made in a root). The entries are in path order. At most limit of them are kept (diffKeep when
// limit is not positive or larger); Total and Counts cover every entry that is not left out. More than diffKeep entries
// are not read into memory: the first diffKeep in the order zfs prints them are sorted and cut.
func ParseDiff(out string, limit int, skip []string) DiffResult {
	if limit <= 0 || limit > diffKeep {
		limit = diffKeep
	}
	res := DiffResult{Entries: []DiffEntry{}, Counts: map[string]int{}}
	for _, l := range strings.Split(out, "\n") {
		f := strings.Split(l, "\t")
		if len(f) < 3 || f[0] == "" {
			continue
		}
		if f[0] == "M" && f[1] == "/" {
			continue
		}
		if len(skip) > 0 && slices.Contains(skip, unescapeDiffPath(f[2])) {
			continue
		}
		res.Total++
		res.Counts[f[0]]++
		if len(res.Entries) >= diffKeep {
			continue
		}
		e := DiffEntry{Kind: f[0], Type: f[1], Path: unescapeDiffPath(f[2])}
		if f[0] == "R" && len(f) > 3 {
			e.To = unescapeDiffPath(f[3])
		}
		res.Entries = append(res.Entries, e)
	}
	sort.SliceStable(res.Entries, func(i, j int) bool { return res.Entries[i].Path < res.Entries[j].Path })
	if len(res.Entries) > limit {
		res.Entries = res.Entries[:limit]
	}
	res.Truncated = res.Total > len(res.Entries)
	return res
}

// unescapeDiffPath decodes the octal escapes of zfs diff: a backslash followed by four octal digits is one byte
// (\0040 is a space).
func unescapeDiffPath(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+4 < len(s) && isOct(s[i+1]) && isOct(s[i+2]) && isOct(s[i+3]) && isOct(s[i+4]) {
			b.WriteByte((s[i+2]-'0')<<6 | (s[i+3]-'0')<<3 | (s[i+4] - '0'))
			i += 4
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isOct(c byte) bool { return c >= '0' && c <= '7' }

// Diff implements Client: `zfs diff -FH from to`, where from is a snapshot and to a dataset (a clone of it) or a
// later snapshot of the same dataset.
func (c *Command) Diff(ctx context.Context, from, to string, limit int, skip []string) (DiffResult, error) {
	if err := ValidateSnapshot(from); err != nil {
		return DiffResult{}, err
	}
	if _, err := ValidateAny(to); err != nil {
		return DiffResult{}, err
	}
	res, err := c.R.Run(ctx, run.Cmd{Name: c.zfs(), Args: []string{"diff", "-FH", from, to}, Timeout: diffTimeout, MaxOutput: 32 << 20})
	if err != nil {
		return DiffResult{}, err
	}
	out := ParseDiff(string(res.Stdout), limit, skip)
	if res.Truncated {
		out.Truncated = true
	}
	return out, nil
}

// Diff implements Client. SetDiff gives the answer of a pair.
func (f *Fake) Diff(_ context.Context, from, to string, limit int, skip []string) (DiffResult, error) {
	if err := ValidateSnapshot(from); err != nil {
		return DiffResult{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.diffs[from+" "+to]
	if !ok {
		return DiffResult{Entries: []DiffEntry{}}, nil
	}
	return ParseDiff(d, limit, skip), nil
}

// SetDiff sets the output of `zfs diff -FH from to` that Diff parses.
func (f *Fake) SetDiff(from, to, output string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.diffs == nil {
		f.diffs = map[string]string{}
	}
	f.diffs[from+" "+to] = output
}
