package service_test

import (
	"fmt"
	"strings"
	"testing"

	"jailops.net/jailops/internal/sys/ifconfig"
	"jailops.net/jailops/sdk/api"
)

// A network without --bridge gets a bridge of its own name when it can; otherwise jo<N>, and the answer says why. A
// --bridge that is taken is refused when the network is created, not when its first jail starts.
func TestTheBridgeOfANetwork(t *testing.T) {
	e, s := newEnv(t)
	ctx := as(globalAdmin, "")
	e.Net.AddIface(ifconfig.Iface{Name: "mybr", Groups: []string{"bridge"}})                           // made by hand
	e.Net.AddIface(ifconfig.Iface{Name: "jo0", Groups: []string{"bridge"}, Description: "customer's"}) // foreign, with a generated-looking name
	sub := 60
	create := func(name, bridge string) (api.Network, error) {
		sub++
		return s.CreateNetwork(ctx, api.Network{Metadata: api.ObjectMeta{Name: name}, Spec: api.NetworkSpec{Bridge: bridge, Uplink: "em0",
			IPv4: &api.IPv4Net{Subnet: fmt.Sprintf("10.%d.0.0/24", sub), Gateway: fmt.Sprintf("10.%d.0.1", sub), Range: fmt.Sprintf("10.%d.0.10-10.%d.0.20", sub, sub), NAT: true}}})
	}
	for _, c := range []struct{ name, bridge, want, note string }{
		{"harbour", "", "harbour", ""},
		{"abcdefghijklm", "", "abcdefghijklm", ""}, // 13: abcdefghijklmga and gb are 15
		{"abcdefghijklmn", "", "jo1", "bridge jo1 (the network name is longer than 13 characters)"},
		{"my-net", "", "jo2", "bridge jo2 (the network name cannot be a bridge name"},
		{"em0", "", "jo3", `driver "em"`},
		{"mybr", "", "jo4", "the interface mybr already exists on this host without a JailOps description"},
		{"office", "lan2", "lan2", ""},
		{"lan2", "", "jo5", `the interface lan2 is used by network "office"`},
		{"harbourga", "", "jo6", `the interface harbourga is used by network "harbour"`},
		{"routed", "dmzbridge", "dmzbridge", ""},
	} {
		n, err := create(c.name, c.bridge)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if n.Spec.Bridge != c.want {
			t.Errorf("%s: bridge %s, want %s", c.name, n.Spec.Bridge, c.want)
		}
		got := strings.Join(n.Status.Warnings, "; ")
		if c.note == "" && got != "" || !strings.Contains(got, c.note) {
			t.Errorf("%s: warnings %q, want %q", c.name, got, c.note)
		}
		// the bridge is the spec's: stored, read back the same
		if g, _ := s.GetNetwork(ctx, c.name); g.Spec.Bridge != c.want {
			t.Errorf("%s: stored bridge %s", c.name, g.Spec.Bridge)
		}
	}
	for _, c := range []struct{ name, bridge, msg string }{
		{"x1", "mybr", "the interface mybr already exists on this host without a JailOps description"},
		{"x2", "jo0", `already exists on this host and is not this network's (its description is "customer's")`},
		{"x3", "vmx0", `driver "vmx"`},
		{"x4", "harbourgb", `the interface harbourgb is used by network "harbour"`},
		{"x5", "harbou", ""},                        // harbouga is free: allowed
		{"x6", "harbourbridge1", "longer than 13"},  // 14
		{"x7", "jo1234567890a", "form of a jail's"}, // its gateway end would look like a jail's epair
		{"x8", "Harbour", "not a valid interface name"},
	} {
		_, err := create(c.name, c.bridge)
		if c.msg == "" {
			if err != nil {
				t.Errorf("%s --bridge %s: %v", c.name, c.bridge, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.msg) {
			t.Errorf("%s --bridge %s: %v, want %q", c.name, c.bridge, err, c.msg)
		}
	}
	// the bridge of a network does not change
	n, _ := s.GetNetwork(ctx, "harbour")
	n.Spec.Bridge = "other"
	if _, err := s.UpdateNetwork(ctx, "harbour", n, 0); !api.IsCode(err, api.CodeInvalid) || !strings.Contains(err.Error(), "cannot change") {
		t.Errorf("bridge changed: %v", err)
	}
}

// The doctor explains an interface that carries a network's bridge name but not JailOps's description.
func TestDoctorExplainsAForeignBridge(t *testing.T) {
	e, s := newEnv(t)
	ctx := as(globalAdmin, "")
	check := func() api.DoctorCheck {
		for _, c := range s.Doctor(ctx).Checks {
			if c.Name == "network.bridges" {
				return c
			}
		}
		t.Fatal("no network.bridges check")
		return api.DoctorCheck{}
	}
	if c := check(); c.Status != api.DoctorOK {
		t.Fatalf("%+v", c)
	}
	if _, err := s.CreateNetwork(ctx, api.Network{Metadata: api.ObjectMeta{Name: "harbour"}, Spec: api.NetworkSpec{
		IPv4: &api.IPv4Net{Subnet: "10.66.0.0/24", Gateway: "10.66.0.1", Range: "10.66.0.10-10.66.0.20"}}}); err != nil {
		t.Fatal(err)
	}
	// made by hand after the network was created (ifconfig bridge create; ifconfig bridge0 name harbour)
	e.Net.AddIface(ifconfig.Iface{Name: "harbour", Groups: []string{"bridge"}})
	e.Net.AddIface(ifconfig.Iface{Name: "harbourgb", Groups: []string{"epair"}, Description: "my tunnel"})
	c := check()
	if c.Status != api.DoctorFail || !strings.Contains(c.Message, "harbour (network harbour) exists without a JailOps description") ||
		!strings.Contains(c.Message, `harbourgb (network harbour) is someone else's (description "my tunnel")`) || !strings.Contains(c.Hint, "ifconfig NAME destroy") {
		t.Fatalf("%+v", c)
	}
}
