package scenario

import (
	"embed"
	"fmt"
	"strings"
	"time"
)

// Note is a free-form comparison row (adapter size, API friction, CGO option).
type Note struct {
	Row    string
	Values map[string]string // keyed by column (library name)
}

// CountLines returns the non-test Go source line count of an embedded adapter.
func CountLines(fs embed.FS) int {
	ents, _ := fs.ReadDir(".")
	n := 0
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), "_test.go") || e.Name() == "src.go" {
			continue
		}
		b, _ := fs.ReadFile(e.Name())
		n += strings.Count(string(b), "\n")
	}
	return n
}

// Table renders a plain-text comparison. Columns are the libs in the given order.
func Table(libs []string, results map[string][]Result, notes []Note) string {
	var rows [][]string
	rows = append(rows, append([]string{"scenario"}, libs...))
	names := []string{}
	seen := map[string]bool{}
	for _, l := range libs {
		for _, r := range results[l] {
			if !seen[r.Name] {
				seen[r.Name] = true
				names = append(names, r.Name)
			}
		}
	}
	for _, n := range names {
		row := []string{n}
		for _, l := range libs {
			cell := "-"
			for _, r := range results[l] {
				if r.Name == n {
					st := "PASS"
					if !r.OK {
						st = "FAIL"
					}
					cell = fmt.Sprintf("%s %s", st, r.Dur.Round(10*time.Millisecond))
				}
			}
			row = append(row, cell)
		}
		rows = append(rows, row)
	}
	metric := func(label, name, f string) {
		row := []string{label}
		any := false
		for _, l := range libs {
			cell := "-"
			for _, r := range results[l] {
				if v, ok := r.Metrics[name]; ok {
					cell = fmt.Sprintf(f, v)
					any = true
				}
			}
			row = append(row, cell)
		}
		if any {
			rows = append(rows, row)
		}
	}
	metric("  election (ms)", "election_ms", "%.0f")
	metric("  failover after leader kill (ms)", "failover_ms", "%.0f")
	metric("  full-cluster restart (ms)", "full_restart_ms", "%.0f")
	metric("  seq ops/s", "seq_ops_s", "%.0f")
	metric("  seq p50 (ms)", "seq_p50_ms", "%.2f")
	metric("  seq p99 (ms)", "seq_p99_ms", "%.2f")
	metric("  parallel ops/s", "par_ops_s", "%.0f")
	for _, n := range notes {
		row := []string{n.Row}
		for _, l := range libs {
			v := n.Values[l]
			if v == "" {
				v = "-"
			}
			row = append(row, v)
		}
		rows = append(rows, row)
	}
	w := make([]int, len(rows[0]))
	for _, r := range rows {
		for i, c := range r {
			w[i] = max(w[i], len([]rune(c)))
		}
	}
	var b strings.Builder
	for k, r := range rows {
		for i, c := range r {
			fmt.Fprintf(&b, "%-*s  ", w[i], c)
		}
		b.WriteString("\n")
		if k == 0 {
			for i := range r {
				b.WriteString(strings.Repeat("-", w[i]) + "  ")
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}
