package scenario

import (
	"strings"
	"testing"
	"time"
)

func TestTable(t *testing.T) {
	res := map[string][]Result{
		"hashicorp": {{Lib: "hashicorp", Name: "partition", OK: true, Dur: 1234 * time.Millisecond}, {Lib: "hashicorp", Name: "throughput", OK: true, Metrics: map[string]float64{"seq_ops_s": 812.4}}},
		"etcd":      {{Lib: "etcd", Name: "partition", OK: false, Dur: 2 * time.Second}},
	}
	out := Table([]string{"hashicorp", "etcd"}, res, []Note{{Row: "adapter LOC", Values: map[string]string{"hashicorp": "250"}}})
	for _, want := range []string{"PASS 1.23s", "FAIL 2s", "812", "adapter LOC", "scenario"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 5 {
		t.Fatal(out)
	}
}
