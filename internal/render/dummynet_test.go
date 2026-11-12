package render

import (
	"fmt"
	"strings"
	"testing"
)

// The same keys always get the same numbers, whatever order they come in, and no two devices share a pipe.
func TestPipeNumbersAreStableAndDistinct(t *testing.T) {
	var keys []PipeKey
	for i := 0; i < 5000; i++ {
		keys = append(keys, PipeKey{Jail: fmt.Sprintf("0b0e7f3e-5c4b-4f6e-9d3e-%012d", i), Device: i % 3})
	}
	a, err := PipeNumbers(keys)
	if err != nil {
		t.Fatal(err)
	}
	rev := append([]PipeKey(nil), keys...)
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	b, err := PipeNumbers(rev)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int]PipeKey{}
	for _, k := range keys {
		n := a[k]
		if n != b[k] {
			t.Fatalf("%v: %d then %d", k, n, b[k])
		}
		if !IsManagedPipe(n) {
			t.Fatalf("%v: pipe %d is outside the range", k, n)
		}
		if o, dup := seen[n]; dup {
			t.Fatalf("pipe %d of %v is also the pipe of %v", n, k, o)
		}
		seen[n] = k
	}
}

func TestPipeNumbersRefuseMoreDevicesThanPipes(t *testing.T) {
	var keys []PipeKey
	for i := 0; i <= PipeSpan; i++ {
		keys = append(keys, PipeKey{Jail: fmt.Sprintf("j%d", i)})
	}
	if _, err := PipeNumbers(keys); err == nil {
		t.Fatal("more devices than pipes accepted")
	}
}

func TestPipeRate(t *testing.T) {
	if PipeRate(10000000) != "10000000bit/s" {
		t.Fatal(PipeRate(10000000))
	}
}

// A device with a limit sends what the jail sends into its pipe with match rules before the filter rules: on the jail's
// own port for the flows the jail starts, and on the gateway interface for the flows that the host or the internet
// starts, where the pipe of the direction into the jail is 0.
func TestShapingMatchRules(t *testing.T) {
	in := pfBase()
	in.Gateways = []string{"jo0g0b"}
	in.Shaping = []Shape{{Iface: "jo0123456789a0a", Gateway: "jo0g0b", Addrs: []string{"10.20.0.10", "2001:db8:1::100"}, Pipe: 40000}}
	s, err := PFAnchor(in)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "pf_anchor_shaping", s)
	out := "match in on jo0123456789a0a all dnpipe 40000"
	into := "match out on jo0g0b from any to { 10.20.0.10, 2001:db8:1::100 } dnpipe (0, 40000)"
	if !strings.Contains(s, out) || !strings.Contains(s, into) {
		t.Fatalf("the match rules are missing:\n%s", s)
	}
	if strings.Index(s, out) > strings.Index(s, "pass ") || strings.Index(s, into) > strings.Index(s, "pass ") {
		t.Fatalf("a match rule comes after the first pass rule:\n%s", s)
	}
	// no shaping, no pipes
	plain, _ := PFAnchor(pfBase())
	if strings.Contains(plain, "dnpipe") {
		t.Fatalf("an anchor without a limit has dnpipe:\n%s", plain)
	}
	// the match rules are not filter rules: the rule list the simulator walks is unchanged
	noShaping := in
	noShaping.Shaping = nil
	b1, _ := BuildAnchor(in)
	b2, _ := BuildAnchor(noShaping)
	if len(b1.Rules) != len(b2.Rules) {
		t.Fatalf("%d rules with a limit, %d without", len(b1.Rules), len(b2.Rules))
	}
}

// The packet that comes out of a port forward's translation is passed by a rule of its own, which carries a label,
// so that the state of the connection can be ended by the label when the forward goes away.
func TestForwardHasALabelledPassRule(t *testing.T) {
	s, err := PFAnchor(pfBase())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"match in on em0 inet proto tcp from any to (em0) port 8080 rdr-to 10.20.0.10 port 80",
		"pass in quick on em0 inet proto tcp from any to 10.20.0.10 port 80 label \"jo-",
		"pass in quick on em0 inet6 proto tcp from any to 2001:db8:1::100 port 80 label \"jo-",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q:\n%s", want, s)
		}
	}
	a, _ := BuildAnchor(pfBase())
	for _, r := range a.Rules {
		if r.Class != IfEpair && r.Class != IfGateway {
			t.Errorf("a rule of the simulator's list is on neither the epair nor the gateway: %+v", r)
		}
	}
}
