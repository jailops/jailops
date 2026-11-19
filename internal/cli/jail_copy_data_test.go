package cli_test

import (
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
)

// `jail copy --data` asks the server for a copy with the data; without it only the specification is copied.
func TestJailCopyWithData(t *testing.T) {
	h := newHarness(t)
	h.seed()
	tr := h.transcript()
	tr.ok("jail", "create", "web", "--image", "freebsd-15", "--storage", "default", "--network", "lan=10.20.0.15", "--label", "tier=front", "--cpuset", "3,2")
	tr.ok("jail", "snapshot", "create", "web", "before")
	tr.ok("jail", "copy", "web", "web2", "--data")
	cp, ok := h.srv.JailIn("main", "web2")
	if !ok || cp.Spec.Network[0].IPv4 == "10.20.0.15" || cp.Spec.Network[0].IPv4 == "" || cp.Metadata.Labels["tier"] != "front" {
		t.Fatalf("the copy: %v %+v", ok, cp.Spec.Network)
	}
	if cp.Spec.Resources.CPUSet != "2-3" {
		t.Fatalf("the copy lost the cpuset: %q", cp.Spec.Resources.CPUSet)
	}
	tr.ok("jail", "copy", "web", "web3", "--data", "--snapshot", "before")
	tr.ok("jail", "copy", "web", "web4", "--data", "--start")
	if j, _ := h.srv.JailIn("main", "web4"); j.Status.Power != api.PowerRunning {
		t.Fatalf("--start: %s", j.Status.Power)
	}
	out := tr.ok("jail", "copy", "web", "web5", "--data", "--snapshot", "before", "--dry-run")
	if !strings.Contains(out, "dry-run: would copy jail web to web5 with its data from snapshot before") {
		t.Fatalf("dry run: %s", out)
	}
	if _, ok := h.srv.JailIn("main", "web5"); ok {
		t.Fatal("a dry run made the copy")
	}
	for _, args := range [][]string{
		{"jail", "copy", "web", "web6", "--data", "--snapshot", "nope"},
		{"jail", "copy", "web", "web6", "--snapshot", "before"},
		{"jail", "copy", "web", "web6", "--data", "--keep-ip"},
		{"jail", "copy", "web", "web6", "--data", "--start", "--no-wait"},
		{"jail", "copy", "web", "web2", "--data"},
		{"jail", "copy", "nope", "web6", "--data"},
	} {
		if _, _, code := tr.do("", args...); code == 0 {
			t.Errorf("%v succeeded", args)
		}
	}
	if _, ok := h.srv.JailIn("main", "web6"); ok {
		t.Fatal("a refused copy made a jail")
	}
	tr.golden("jail_copy_data")
}
