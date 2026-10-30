package config

import (
	"strings"
	"testing"
)

// netFleet is a fleet with one host, a project that is granted network "n" and storage "s", and the given network spec.
func netFleet(t *testing.T, netSpec string) (string, error) {
	t.Helper()
	d := t.TempDir()
	write(t, d, "fleet.yaml", "id: f\n")
	write(t, d, "hosts/a/host.yaml", host("a", ""))
	write(t, d, "hosts/a/storage/s.yaml", "apiVersion: jailops/v1\nkind: Storage\nmetadata:\n  name: s\nspec:\n  dataset: tank/jo\n")
	write(t, d, "shared/networks/n.yaml", "apiVersion: jailops/v1\nkind: Network\nmetadata:\n  name: n\nspec:\n"+netSpec)
	project(t, d, "p", "[a]", "  networks: [{network: n}]\n  storage: [{storage: s}]\n")
	f, err := Load(d)
	if err != nil {
		return "", err
	}
	err = f.Check()
	return strings.Join(f.Warnings, "\n"), err
}

func TestProxyAndRoutedNetworksAreCheckedOffline(t *testing.T) {
	ok := `  uplink: vtnet0
  ipv6:
    mode: proxy
    subnet: "2001:db8:1:2:100::/80"
    gateway: "fe80::1"
    range: "2001:db8:1:2:100::10-2001:db8:1:2:100::ff"
`
	if _, err := netFleet(t, ok); err != nil {
		t.Fatalf("a valid proxy network: %v", err)
	}
	routed := `  uplink: vtnet0
  ipv6:
    mode: routed
    subnet: "2001:db8:1:2:1::/80"
    gateway: "2001:db8:1:2:1::1"
    range: "2001:db8:1:2:1::10-2001:db8:1:2:1::ff"
`
	if _, err := netFleet(t, routed); err != nil {
		t.Fatalf("a valid routed network: %v", err)
	}
	for name, tc := range map[string]struct{ spec, want string }{
		"proxy without uplink":      {strings.Replace(ok, "  uplink: vtnet0\n", "", 1), "needs an uplink"},
		"proxy with routed gateway": {strings.Replace(ok, `"fe80::1"`, `"2001:db8:1:2:100::1"`, 1), "link-local"},
		"proxy /32":                 {strings.NewReplacer("2001:db8:1:2:100::/80", "2001:db8::/32").Replace(ok), "between /48 and /126"},
		"host bits":                 {strings.Replace(ok, "100::/80", "100::1/80", 1), "host bits set"},
		"range outside":             {strings.Replace(ok, "100::10-2001:db8:1:2:100::ff", "200::10-2001:db8:1:2:200::ff", 1), "outside"},
		"private without uplink":    {"  private: true\n  ipv4: {subnet: 10.1.0.0/24, gateway: 10.1.0.1, range: 10.1.0.2-10.1.0.9}\n", "set an uplink"},
		"routed gateway elsewhere":  {strings.Replace(routed, `"2001:db8:1:2:1::1"`, `"2001:db8:9::1"`, 1), "outside"},
	} {
		if _, err := netFleet(t, tc.spec); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: want %q, got %v", name, tc.want, err)
		}
	}
}

func TestOverlappingNetworksAreRefusedOffline(t *testing.T) {
	d := t.TempDir()
	write(t, d, "fleet.yaml", "id: f\n")
	write(t, d, "hosts/a/host.yaml", host("a", ""))
	write(t, d, "shared/networks/one.yaml", "apiVersion: jailops/v1\nkind: Network\nmetadata:\n  name: one\nspec:\n  ipv4: {subnet: 10.1.0.0/24, gateway: 10.1.0.1, range: 10.1.0.2-10.1.0.9}\n")
	write(t, d, "shared/networks/two.yaml", "apiVersion: jailops/v1\nkind: Network\nmetadata:\n  name: two\nspec:\n  ipv4: {subnet: 10.1.0.128/25, gateway: 10.1.0.129, range: 10.1.0.130-10.1.0.140}\n")
	f, err := Load(d)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Check(); err == nil || !strings.Contains(err.Error(), "overlaps network") {
		t.Fatalf("got %v", err)
	}
}

func TestProjectAndJailRulesOfTheDaemonAreChecked(t *testing.T) {
	d := t.TempDir()
	write(t, d, "fleet.yaml", "id: f\n")
	write(t, d, "hosts/a/host.yaml", host("a", ""))
	write(t, d, "hosts/a/storage/s.yaml", "apiVersion: jailops/v1\nkind: Storage\nmetadata:\n  name: s\nspec:\n  dataset: tank/jo\n  encrypted: true\n")
	write(t, d, "shared/networks/n.yaml", "apiVersion: jailops/v1\nkind: Network\nmetadata:\n  name: n\nspec:\n  ipv4: {subnet: 10.1.0.0/24, gateway: 10.1.0.1, range: 10.1.0.2-10.1.0.9}\n")
	write(t, d, "shared/images/i.yaml", "apiVersion: jailops/v1\nkind: Image\nmetadata:\n  name: i\nspec:\n  os: freebsd\n  release: 15.1-RELEASE\n  arch: amd64\n  source: {type: pkgbase}\n")
	project(t, d, "p", "[a]", "  networks: [{network: n}]\n  storage: [{storage: s}]\n")
	write(t, d, "projects/p/jails/j.yaml", "apiVersion: jailops/v1\nkind: Jail\nhosts: [a]\nmetadata:\n  name: j\nspec:\n  image: i@stable\n  storage: s\n  encrypted: true\n  packages: [nginx]\n  network: [{network: n}]\n  host_mounts:\n    - {source: /hdd/a, target: /mnt/a}\n    - {source: /hdd/b, target: /mnt/a, read_only: true}\n")
	f, err := Load(d)
	if err != nil {
		t.Fatal(err)
	}
	err = f.Check()
	if err == nil {
		t.Fatal("accepted")
	}
	for _, want := range []string{"spec.encrypted is reserved: native ZFS encryption is not implemented", "spec.encrypted is reserved: native ZFS encryption of jails", "spec.packages is reserved", "duplicate mount point"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in:\n%v", want, err)
		}
	}
	w := strings.Join(f.Warnings, "\n")
	if !strings.Contains(w, "read-write") || !strings.Contains(w, "admin-host-mount") {
		t.Errorf("host mount warnings:\n%s", w)
	}
}

func TestProfilesCannotCarryAdminOnlyKeys(t *testing.T) {
	d := base(t)
	write(t, d, "profiles/x.yaml", "apiVersion: jailops/v1\nkind: Profile\nmetadata:\n  name: x\nspec:\n  host_mounts:\n    - {source: /a, target: /b}\n")
	if _, err := Load(d); err == nil || !strings.Contains(err.Error(), "cannot carry host_mounts") {
		t.Fatalf("got %v", err)
	}
}

func TestPlainHTTPHostIsLoopbackOnly(t *testing.T) {
	for url, ok := range map[string]bool{"http://127.0.0.1:8443": true, "http://localhost:1": true, "http://[::1]:1": true, "http://192.0.2.1:8443": false, "http://web1.example.org": false, "https://web1.example.org:8443": true} {
		d := t.TempDir()
		write(t, d, "fleet.yaml", "id: f\n")
		write(t, d, "hosts/a/host.yaml", "apiVersion: jailops/v1\nkind: Host\nmetadata:\n  name: a\nspec:\n  url: "+url+"\n")
		_, err := Load(d)
		if (err == nil) != ok {
			t.Errorf("%s: err=%v want ok=%v", url, err, ok)
		}
	}
}

func TestNewNetworkIsNotDeclarable(t *testing.T) {
	d := base(t)
	project(t, d, "p", "[a]", "  new_network: true\n")
	if _, err := Load(d); err == nil || !strings.Contains(err.Error(), "new_network") {
		t.Fatalf("got %v", err)
	}
}

func TestOverlappingStorageIsRefusedOffline(t *testing.T) {
	d := base(t)
	for n, ds := range map[string]string{"a1": "tank/x", "a2": "tank/x/y"} {
		write(t, d, "hosts/a/storage/"+n+".yaml", "apiVersion: jailops/v1\nkind: Storage\nmetadata:\n  name: "+n+"\nspec:\n  dataset: "+ds+"\n")
	}
	f, err := Load(d)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Check(); err == nil || !strings.Contains(err.Error(), "datasets may not be equal or nested") {
		t.Fatalf("got %v", err)
	}
}

func TestNamesFollowTheDaemonRules(t *testing.T) {
	d := base(t)
	write(t, d, "shared/networks/my_net.yaml", "apiVersion: jailops/v1\nkind: Network\nmetadata:\n  name: my_net\nspec:\n  ipv4: {subnet: 10.1.0.0/24, gateway: 10.1.0.1, range: 10.1.0.2-10.1.0.9}\n")
	project(t, d, "p", "[a]", "")
	write(t, d, "projects/p/address-sets/web-tier.yaml", "apiVersion: jailops/v1\nkind: AddressSet\nmetadata:\n  name: web-tier\nspec:\n  addresses: [192.0.2.1]\n")
	f, err := Load(d)
	if err != nil {
		t.Fatal(err)
	}
	err = f.Check()
	if err == nil || !strings.Contains(err.Error(), `Network name "my_net"`) || !strings.Contains(err.Error(), `address set name "web-tier"`) {
		t.Fatalf("got %v", err)
	}
}

func wgFleet(t *testing.T, files map[string]string) error {
	t.Helper()
	d := t.TempDir()
	write(t, d, "fleet.yaml", "id: f\n")
	write(t, d, "hosts/a/host.yaml", host("a", ""))
	write(t, d, "shared/networks/lan.yaml", "apiVersion: jailops/v1\nkind: Network\nmetadata:\n  name: lan\nspec:\n  ipv4: {subnet: 10.20.0.0/24, gateway: 10.20.0.1, range: 10.20.0.2-10.20.0.9}\n")
	for p, c := range files {
		write(t, d, p, c)
	}
	f, err := Load(d)
	if err != nil {
		return err
	}
	return f.Check()
}

func wgDoc(name, spec string) string {
	return "apiVersion: jailops/v1\nkind: WireGuard\nmetadata:\n  name: " + name + "\nspec:\n" + spec
}

func TestWireGuardIsCheckedOffline(t *testing.T) {
	key1, key2 := "AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=", "AgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgI="
	ok := "  mode: server\n  address: [10.99.0.1/24]\n  networks: [lan]\n  peers:\n    - {name: laptop, public_key: " + key1 + ", allowed_ips: [10.99.0.2/32]}\n"
	if err := wgFleet(t, map[string]string{"hosts/a/wireguard/office.yaml": wgDoc("office", ok)}); err != nil {
		t.Fatalf("a valid server: %v", err)
	}
	for name, tc := range map[string]struct {
		files map[string]string
		want  string
	}{
		"client":        {map[string]string{"hosts/a/wireguard/c.yaml": wgDoc("c", "  mode: client\n  address: [10.99.0.2/32]\n  endpoint: vpn.example.com:51820\n  peer_public_key: "+key1+"\n  allowed_ips: [10.99.0.0/24]\n")}, "cannot be declared"},
		"private key":   {map[string]string{"hosts/a/wireguard/office.yaml": wgDoc("office", ok+"  private_key: "+key2+"\n")}, "private_key"},
		"shared":        {map[string]string{"shared/wireguard/office.yaml": wgDoc("office", ok)}, ""},
		"bad key":       {map[string]string{"hosts/a/wireguard/office.yaml": wgDoc("office", strings.Replace(ok, key1, "not-a-key", 1))}, "not a WireGuard public key"},
		"no address":    {map[string]string{"hosts/a/wireguard/office.yaml": wgDoc("office", strings.Replace(ok, "  address: [10.99.0.1/24]\n", "", 1))}, "spec.address"},
		"own address":   {map[string]string{"hosts/a/wireguard/office.yaml": wgDoc("office", strings.Replace(ok, "10.99.0.2/32", "10.99.0.1/32", 1))}, "server's own address"},
		"default route": {map[string]string{"hosts/a/wireguard/office.yaml": wgDoc("office", strings.Replace(ok, "10.99.0.2/32", "0.0.0.0/0", 1))}, "not a peer address"},
		"no network":    {map[string]string{"hosts/a/wireguard/office.yaml": wgDoc("office", strings.Replace(ok, "[lan]", "[dmz]", 1))}, "unknown Network \"dmz\""},
		"long name":     {map[string]string{"hosts/a/wireguard/headquarters-a.yaml": wgDoc("headquarters-a", ok)}, "set spec.interface"},
		"same port": {map[string]string{"hosts/a/wireguard/office.yaml": wgDoc("office", ok),
			"hosts/a/wireguard/lab.yaml": wgDoc("lab", "  mode: server\n  address: [10.98.0.1/24]\n")}, "listen port 51820"},
		"overlap": {map[string]string{"hosts/a/wireguard/office.yaml": wgDoc("office", ok),
			"hosts/a/wireguard/lab.yaml": wgDoc("lab", "  mode: server\n  listen_port: 51821\n  address: [10.99.0.129/25]\n")}, "overlaps tunnel"},
		"twice": {map[string]string{"hosts/a/wireguard/office.yaml": wgDoc("office", ok+"    - {name: laptop, public_key: "+key2+", allowed_ips: [10.99.0.3/32]}\n")}, "listed twice"},
	} {
		err := wgFleet(t, tc.files)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: want %q, got %v", name, tc.want, err)
		}
	}
}
