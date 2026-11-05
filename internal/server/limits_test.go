package server_test

import (
	"strings"
	"testing"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

// wantRefused asserts an error of the code whose message says what.
func wantRefused(t *testing.T, err error, code api.Code, what ...string) {
	t.Helper()
	wantCode(t, err, code)
	for _, w := range what {
		if !strings.Contains(err.Error(), w) {
			t.Fatalf("%q does not say %q", err, w)
		}
	}
}

// Sizes are stored and written as bytes: "4GiB" was accepted by the API and refused by the helper at the first
// jail create (a project quota) or inside the create operation (a jail's disk).
func TestSizesAreStoredAsBytes(t *testing.T) {
	e := seeded(t)
	c := ctxT(t)
	if _, err := e.Admin.Projects().Patch(c, dtest.SeedProject, map[string]any{"spec": map[string]any{
		"storage": []map[string]any{{"storage": "default", "quota": "4GiB"}}, "quotas": map[string]any{"memory": "512m"}}}, ""); err != nil {
		t.Fatal(err)
	}
	p, err := e.Admin.Projects().Get(c, dtest.SeedProject)
	if err != nil || p.Spec.Storage[0].Quota != "4294967296" || p.Spec.Quotas.Memory != "536870912" {
		t.Fatalf("%v %+v", err, p.Spec)
	}
	q, _ := e.ZFS.Get(c, "tank/jailops/projects/"+dtest.SeedProject, "quota")
	if q["quota"].Value != "4294967296" {
		t.Fatalf("zfs got %v", q)
	}
	sp := webSpec()
	sp.Resources.Disk, sp.Resources.DiskReservation, sp.Resources.Memory = "1k", "7B", "256M"
	sp.Volumes = []api.VolumeMount{{Name: "data", Mount: "/data", Quota: "512m"}}
	op, err := e.Admin.Jails().Create(c, sdk.NewJail("web", sp))
	if err != nil {
		t.Fatal(err)
	}
	j, err := op.Wait(c)
	if err != nil {
		t.Fatalf("%v\n%s", err, e.Log())
	}
	if r := j.Spec.Resources; r.Disk != "1024" || r.DiskReservation != "7" || j.Spec.Volumes[0].Quota != "536870912" {
		t.Fatalf("%+v %+v", r, j.Spec.Volumes)
	}
	root := "tank/jailops/projects/" + dtest.SeedProject + "/jails/web/root"
	if q, _ := e.ZFS.Get(c, root, "quota"); q["quota"].Value != "1024" {
		t.Fatalf("root quota %v", q)
	}
	// larger than the helper sets, or not a size: refused by the API
	_, err = e.Admin.Projects().Patch(c, dtest.SeedProject, map[string]any{"spec": map[string]any{"storage": []map[string]any{{"storage": "default", "quota": "17T"}}}}, "")
	wantRefused(t, err, api.CodeInvalid, "16T")
	sp.Resources.Disk = "4 GiB"
	_, err = e.Admin.Jails().Create(c, sdk.NewJail("web2", sp))
	wantRefused(t, err, api.CodeInvalid, "disk")
}

// An attachment cannot have another MTU than its network: the bridge gives every port its own.
func TestAttachmentMTUMustBeTheNetworks(t *testing.T) {
	e := seeded(t)
	c := ctxT(t)
	sp := webSpec()
	sp.Network[0].MTU = 9000
	_, err := e.Admin.Jails().Create(c, sdk.NewJail("web", sp))
	wantRefused(t, err, api.CodeInvalid, "9000", "1500", "lan")
	sp.Network[0].MTU = 1500
	op, err := e.Admin.Jails().Create(c, sdk.NewJail("web", sp))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := op.Wait(c); err != nil {
		t.Fatal(err)
	}
}

// A network is checked against the host's interfaces when it is created: its subnet is not one the host has, its
// uplink exists and can carry it, and a proxy-mode subnet lies in the uplink's prefix.
func TestNetworksAreCheckedAgainstTheHost(t *testing.T) {
	e := dtest.New(t, dtest.Options{})
	c := ctxT(t)
	g := e.Global()
	v4 := func(name, sub, gw, rng, uplink string) api.Network {
		return api.Network{Metadata: api.ObjectMeta{Name: name}, Spec: api.NetworkSpec{Uplink: uplink,
			IPv4: &api.IPv4Net{Subnet: sub, Gateway: gw, Range: rng, NAT: uplink != ""}}}
	}
	// em0 is in 203.0.113.0/24
	_, err := g.Networks().Create(c, v4("lan", "203.0.113.0/25", "203.0.113.1", "203.0.113.10-203.0.113.20", ""))
	wantRefused(t, err, api.CodeConflict, "203.0.113.2/24", "em0")
	_, err = g.Networks().Create(c, v4("lan", "10.40.0.0/24", "10.40.0.1", "10.40.0.10-10.40.0.20", "em9"))
	wantRefused(t, err, api.CodeInvalid, "em9 does not exist")
	if ae := err.(*api.Error); !strings.Contains(ae.Hint, "em0 (203.0.113.2/24") {
		t.Fatalf("the hint names no candidate: %q", ae.Hint)
	}
	_, err = g.Networks().Create(c, v4("lan", "10.40.0.0/24", "10.40.0.1", "10.40.0.10-10.40.0.20", "lo0"))
	wantRefused(t, err, api.CodeInvalid, "lo0")
	if _, err := g.Networks().Create(c, v4("lan", "10.40.0.0/24", "10.40.0.1", "10.40.0.10-10.40.0.20", "em1")); err != nil {
		t.Fatal(err)
	}
	// a routed IPv6 prefix inside the uplink's on-link prefix, and a proxy subnet outside it
	v6 := func(name string, mode api.IPv6Mode, sub, gw, rng string) api.Network {
		return api.Network{Metadata: api.ObjectMeta{Name: name}, Spec: api.NetworkSpec{Uplink: "em0",
			IPv6: &api.IPv6Net{Mode: mode, Subnet: sub, Gateway: gw, Range: rng}}}
	}
	_, err = g.Networks().Create(c, v6("rt", api.IPv6Routed, "2001:db8:1:2:7::/80", "2001:db8:1:2:7::1", "2001:db8:1:2:7::10-2001:db8:1:2:7::ff"))
	wantRefused(t, err, api.CodeConflict, "em0")
	_, err = g.Networks().Create(c, v6("sw", api.IPv6Proxy, "2001:db8:5::/80", "fe80::1", "2001:db8:5::10-2001:db8:5::ff"))
	wantRefused(t, err, api.CodeInvalid, "2001:db8:1:2::/64")
	if _, err := g.Networks().Create(c, v6("sw", api.IPv6Proxy, "2001:db8:1:2:5::/80", "fe80::1", "2001:db8:1:2:5::10-2001:db8:1:2:5::ff")); err != nil {
		t.Fatal(err)
	}
	// an update is checked too
	_, err = g.Networks().Patch(c, "lan", map[string]any{"spec": map[string]any{"uplink": "em9"}}, "")
	wantRefused(t, err, api.CodeInvalid, "em9")
}

// A network that routes says at once when the host does not forward.
func TestNetworkCreateWarnsWhenForwardingIsOff(t *testing.T) {
	e := dtest.New(t, dtest.Options{})
	c := ctxT(t)
	e.SetSysctl("net.inet.ip.forwarding", "0")
	n := dtest.LAN()
	n.Spec.Uplink, n.Spec.IPv4.NAT = "em0", true
	got, err := e.Global().Networks().Create(c, n)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Status.Warnings) != 1 || !strings.Contains(got.Status.Warnings[0], "sysctl net.inet.ip.forwarding=1") || !strings.Contains(got.Status.Warnings[0], "gateway_enable=YES") {
		t.Fatalf("%q", got.Status.Warnings)
	}
	// a network without an uplink routes nothing and is not warned about
	n2 := dtest.LAN()
	n2.Metadata.Name = "inner"
	n2.Spec.IPv4 = &api.IPv4Net{Subnet: "10.21.0.0/24", Gateway: "10.21.0.1", Range: "10.21.0.10-10.21.0.50"}
	if got, err := e.Global().Networks().Create(c, n2); err != nil || len(got.Status.Warnings) != 0 {
		t.Fatalf("%v %q", err, got.Status.Warnings)
	}
}

// The names of new objects are limited so that what JailOps derives from them fits; the error names the limit.
func TestNewNamesAreLimited(t *testing.T) {
	e := seeded(t)
	c := ctxT(t)
	g := e.Global()
	_, err := g.Projects().Create(c, api.Project{Metadata: api.ObjectMeta{Name: "abcdefghijklmn"}})
	wantRefused(t, err, api.CodeInvalid, "at most 13")
	if _, err := g.Projects().Create(c, api.Project{Metadata: api.ObjectMeta{Name: "abcdefghijklm"}}); err != nil {
		t.Fatal(err)
	}
	_, err = e.Admin.Jails().Create(c, sdk.NewJail(strings.Repeat("j", 33), webSpec()))
	wantRefused(t, err, api.CodeInvalid, "at most 32")
	sp := webSpec()
	sp.Volumes = []api.VolumeMount{{Name: strings.Repeat("v", 33), Mount: "/data"}}
	_, err = e.Admin.Jails().Create(c, sdk.NewJail("web", sp))
	wantRefused(t, err, api.CodeInvalid, "volume", "at most 32")
	_, err = g.Images().Create(c, api.Image{Metadata: api.ObjectMeta{Name: strings.Repeat("i", 33)}, Spec: api.ImageSpec{OS: api.OSFreeBSD, Release: "15.0-RELEASE", Source: api.ImageSource{Type: "pkgbase"}}})
	wantRefused(t, err, api.CodeInvalid, "at most 32")
	_, err = g.Storage().Create(c, api.Storage{Metadata: api.ObjectMeta{Name: "deep"}, Spec: api.StorageSpec{Dataset: "tank/" + strings.Repeat("d", 84)}})
	wantRefused(t, err, api.CodeInvalid, "at most 88")
	j := mustJail(t, e, strings.Repeat("j", 32))
	snaps := e.Admin.Jails().Snapshots(j.Metadata.Name)
	_, err = snaps.Create(c, strings.Repeat("s", 70), "")
	wantRefused(t, err, api.CodeInvalid, "70 characters", "at most 64")
	_, err = snaps.Create(c, "jo-mine", "")
	wantRefused(t, err, api.CodeInvalid, "reserved")
	op, err := snaps.Create(c, strings.Repeat("s", 64), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := op.Wait(c); err != nil {
		t.Fatal(err)
	}
	_, err = g.WireGuard().Create(c, api.WireGuard{Metadata: api.ObjectMeta{Name: "headquarters1"}, Spec: api.WireGuardSpec{
		Mode: api.WireGuardServer, Address: []string{"10.99.0.1/24"}, Networks: []string{"lan"}, Endpoint: "vpn.example.com:51820"}})
	wantRefused(t, err, api.CodeInvalid, "longer than 12 characters: give --interface")
	if _, err := g.WireGuard().Create(c, api.WireGuard{Metadata: api.ObjectMeta{Name: "headquarters1"}, Spec: api.WireGuardSpec{Interface: "wghq",
		Mode: api.WireGuardServer, Address: []string{"10.99.0.1/24"}, Networks: []string{"lan"}, Endpoint: "vpn.example.com:51820"}}); err != nil {
		t.Fatal(err)
	}
}
