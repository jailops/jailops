package model

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"jailops.net/jailops/sdk/api"
)

func TestMergeFollowsRFC7386(t *testing.T) {
	dst := map[string]any{"a": 1, "m": map[string]any{"x": 1, "y": 2}, "l": []any{1, 2}, "gone": "x"}
	src := map[string]any{"a": 2, "m": map[string]any{"y": 3, "z": 4}, "l": []any{9}, "gone": nil}
	got := Merge(dst, src)
	want := map[string]any{"a": 2, "m": map[string]any{"x": 1, "y": 3, "z": 4}, "l": []any{9}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestMergeDoesNotAliasSource(t *testing.T) {
	tmpl := map[string]any{"m": map[string]any{"x": 1}}
	a := Merge(map[string]any{}, CloneMap(tmpl))
	a["m"].(map[string]any)["x"] = 99
	if tmpl["m"].(map[string]any)["x"] != 1 {
		t.Fatal("the template was modified through a merged copy")
	}
}

func TestDecodeStrictRejectsUnknownKeys(t *testing.T) {
	_, err := DecodeSpec(api.KindJail, map[string]any{"image": "i", "imge": "typo"})
	if err == nil || !strings.Contains(err.Error(), "imge") {
		t.Fatalf("got %v", err)
	}
	if _, err := DecodeSpec(api.KindRemote, map[string]any{}); err == nil {
		t.Fatal("an unmanaged kind decoded")
	}
}

func TestNames(t *testing.T) {
	for _, ok := range []string{"a", "web-1"} {
		if err := CheckName(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "A", "-a", ".a", "a b", "a/b", "x_y.z", "0day", "web-", strings.Repeat("a", 64)} {
		if CheckName(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if CheckSimpleName("a.b") == nil {
		t.Error("dots allowed in simple names")
	}
}

func TestRolloutSpec(t *testing.T) {
	r := RolloutSpec{Selector: map[string]string{"a": "b"}, Pause: "30s", HealthGate: HealthGate{Timeout: "1m"}}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	p, to, iv, _ := r.Durations()
	if p != 30*time.Second || to != time.Minute || iv != 2*time.Second {
		t.Fatalf("%v %v %v", p, to, iv)
	}
	for name, bad := range map[string]RolloutSpec{
		"no selector": {},
		"action":      {Selector: map[string]string{"a": "b"}, Action: "delete"},
		"generation":  {Selector: map[string]string{"a": "b"}, Generation: "3"},
		"duration":    {Selector: map[string]string{"a": "b"}, Pause: "soon"},
		"negative":    {Selector: map[string]string{"a": "b"}, Canary: -1},
	} {
		if bad.Validate() == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// A file of a fleet repository written for an earlier release is read as the current shapes, and the notes say what to
// change; a key that never was valid is still an error.
func TestDecodeSpecReadsTheShapesOfEarlierReleases(t *testing.T) {
	raw := map[string]any{
		"ipv4": map[string]any{"subnet": "10.0.0.0/24", "gateway": "10.0.0.1", "range": "10.0.0.2-10.0.0.9"},
		"ipv6": map[string]any{"mode": "proxy", "subnet": "2001:db8:1::/64", "range": "2001:db8:1::10-2001:db8:1::ff"},
	}
	spec, notes, err := DecodeSpecNotes(api.KindNetwork, raw)
	if err != nil {
		t.Fatalf("a network with ipv6.mode: %v", err)
	}
	if ns := spec.(api.NetworkSpec); ns.IPv6 == nil || !ns.IPv6.NDPProxy {
		t.Errorf("the proxy mode is not the NDP proxy: %+v", ns.IPv6)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "ndp_proxy") {
		t.Errorf("notes %q", notes)
	}
	// the document the caller read is left as it was
	if _, has := raw["ipv6"].(map[string]any)["mode"]; !has {
		t.Error("the caller's document was rewritten")
	}
	if _, err := DecodeSpec(api.KindWireGuard, map[string]any{"mode": "server", "address": []any{"10.99.0.1/24"}, "down": true}); err != nil {
		t.Errorf("a tunnel with down: %v", err)
	}
	if _, err := DecodeSpec(api.KindJail, map[string]any{"image": "freebsd-15-1@stable", "proxy": []any{map[string]any{"name": "p", "listen": ":80", "jail_port": 80}}}); err != nil {
		t.Errorf("a jail with an alias and a proxy device: %v", err)
	}
	// what never was valid stays an error
	if _, err := DecodeSpec(api.KindNetwork, map[string]any{"ipv6": map[string]any{"ndp_prox": true}}); err == nil {
		t.Error("a misspelt key was accepted")
	}
	// a redirect rule of a policy cannot be rewritten on its own: the error says what to write
	_, err = DecodeSpec(api.KindFirewallPolicy, map[string]any{"default_in": "deny", "default_out": "allow", "rules": []any{
		map[string]any{"action": "allow", "direction": "in", "proto": "tcp", "ports": []any{"80"}, "redirect": map[string]any{"host_port": 8080, "jail_port": 80}}}})
	if err == nil || !strings.Contains(err.Error(), "expose") || strings.Contains(err.Error(), "unknown field") {
		t.Errorf("a redirect rule: %v", err)
	}
}
