package priv

import (
	"fmt"
	"strings"
)

// diffContext is the number of unchanged lines around a change in a hunk.
const diffContext = 3

// maxDiffCells bounds the table of the line comparison; larger pairs of files are shown as one hunk that replaces
// every line.
const maxDiffCells = 4 << 20

// splitLines splits text into lines without their newline; a last line without a newline is kept.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	l := strings.Split(s, "\n")
	if l[len(l)-1] == "" {
		l = l[:len(l)-1]
	}
	return l
}

type diffOp struct {
	kind byte // ' ', '-', '+'
	text string
	a, b int // line numbers (0-based) in old and new
}

// diffLines returns the edit script from a to b by longest common subsequence.
func diffLines(a, b []string) []diffOp {
	n, m := len(a), len(b)
	if n*m > maxDiffCells {
		var out []diffOp
		for i, s := range a {
			out = append(out, diffOp{'-', s, i, 0})
		}
		for j, s := range b {
			out = append(out, diffOp{'+', s, n, j})
		}
		return out
	}
	// lcs[i][j] is the length of the common subsequence of a[i:] and b[j:].
	lcs := make([][]int32, n+1)
	for i := range lcs {
		lcs[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var out []diffOp
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && a[i] == b[j]:
			out = append(out, diffOp{' ', a[i], i, j})
			i++
			j++
		case i < n && (j == m || lcs[i+1][j] >= lcs[i][j+1]):
			out = append(out, diffOp{'-', a[i], i, j})
			i++
		default:
			out = append(out, diffOp{'+', b[j], i, j})
			j++
		}
	}
	return out
}

// unifiedDiff renders the change of one file as a unified diff with three lines of context; it is empty when the
// contents are equal. A file that does not exist is /dev/null on its side.
func unifiedDiff(path, before, after string, oldExists, newExists bool) string {
	if before == after && oldExists == newExists {
		return ""
	}
	ops := diffLines(splitLines(before), splitLines(after))
	var b strings.Builder
	from, to := path, path
	if !oldExists {
		from = "/dev/null"
	}
	if !newExists {
		to = "/dev/null"
	}
	fmt.Fprintf(&b, "--- %s\n+++ %s\n", from, to)
	for start := 0; start < len(ops); {
		// find the next change
		c := start
		for c < len(ops) && ops[c].kind == ' ' {
			c++
		}
		if c == len(ops) {
			break
		}
		lo := max(c-diffContext, start)
		// extend the hunk while the next change is close enough to share context
		hi := c
		for k := c; k < len(ops); k++ {
			if ops[k].kind != ' ' {
				hi = k
				continue
			}
			if k-hi > 2*diffContext {
				break
			}
		}
		hi = min(hi+diffContext, len(ops)-1)
		var oldN, newN int
		for _, o := range ops[lo : hi+1] {
			if o.kind != '+' {
				oldN++
			}
			if o.kind != '-' {
				newN++
			}
		}
		oldStart, newStart := ops[lo].a+1, ops[lo].b+1
		if oldN == 0 {
			oldStart--
		}
		if newN == 0 {
			newStart--
		}
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", oldStart, oldN, newStart, newN)
		for _, o := range ops[lo : hi+1] {
			b.WriteByte(o.kind)
			b.WriteString(o.text)
			b.WriteByte('\n')
		}
		start = hi + 1
	}
	return b.String()
}
