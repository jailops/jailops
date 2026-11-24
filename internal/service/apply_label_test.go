package service

import (
	"strings"
	"testing"
)

// A run step is named by its argv on one line: a script in the argv would repeat in full in every report.
func TestRunLabelStopsAtTheFirstLine(t *testing.T) {
	if got := runLabel([]string{"/usr/local/sbin/nginx", "-t"}); got != "/usr/local/sbin/nginx -t" {
		t.Fatalf("%q", got)
	}
	got := runLabel([]string{"sh", "-c", "set -e\nfind /x -delete\necho done"})
	if strings.Contains(got, "\n") || got != "sh -c set -e ..." {
		t.Fatalf("%q", got)
	}
	if got := runLabel([]string{strings.Repeat("a", 300)}); len(got) != 123 || !strings.HasSuffix(got, "...") {
		t.Fatalf("%d %q", len(got), got)
	}
}
