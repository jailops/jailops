package api

import (
	"reflect"
	"strings"
	"testing"
)

func TestRegistryIsConsistent(t *testing.T) {
	seen := map[string]bool{}
	for _, k := range ConfigKeys() {
		if k.Name == "" || k.Description == "" || k.Type == "" || k.Path == "" || seen[k.Name] {
			t.Errorf("bad entry %+v", k)
		}
		seen[k.Name] = true
		if k.Type == "enum" && len(k.Values) == 0 {
			t.Errorf("%s: an enum without values", k.Name)
		}
	}
	for _, want := range []string{"resources.memory", "resources.cpu", "resources.pids", "boot.autostart", "boot.priority", "boot.depends", "image", "hostname",
		"update.mode", "firewall.policies", "network.<name>.mtu", "network.<name>.network", "devfs.<name>.device", "env.<var>", "profiles"} {
		if !seen[want] {
			t.Errorf("missing key %s", want)
		}
	}
	// A returned slice is a copy.
	ConfigKeys()[0].Name = "x"
	if ConfigKeys()[0].Name == "x" {
		t.Fatal("the registry was modified through its listing")
	}
}

func TestApplyConfigSetsAndReportsRestarts(t *testing.T) {
	s := JailSpec{Image: "fb"}
	restart, err := ApplyConfig(&s, map[string]string{
		"resources.memory": "2G", "resources.cpu": "2", "boot.autostart": "true", "boot.depends": "db, cache",
		"network.eth0.network": "lan", "network.eth0.ipv4": "auto", "network.eth0.mtu": "1400", "network.eth1.network": "dmz",
		"volumes.data.mount": "/var/db", "volumes.data.read_only": "yes",
		"devfs.t.device": "tun", "env.TZ": "UTC",
		"firewall.policies": "web, ssh-office", "update.mode": "notify",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.Resources.Memory != "2G" || s.Resources.CPU != 2 || !s.Boot.Autostart || !reflect.DeepEqual(s.Boot.Depends, []string{"db", "cache"}) {
		t.Fatalf("%+v", s)
	}
	if !reflect.DeepEqual(s.Firewall.Policies, []string{"web", "ssh-office"}) {
		t.Fatalf("%+v", s.Firewall)
	}
	if len(s.Network) != 2 || s.Network[0].Name != "eth0" || s.Network[0].MTU != 1400 || s.Network[1].Name != "eth1" || s.Network[1].Network != "dmz" {
		t.Fatalf("%+v", s.Network)
	}
	if len(s.Volumes) != 1 || !s.Volumes[0].ReadOnly || s.Volumes[0].Mount != "/var/db" {
		t.Fatalf("%+v", s.Volumes)
	}
	if len(s.Devfs) != 1 || s.Devfs[0].Device != "tun" || s.Env["TZ"] != "UTC" {
		t.Fatalf("%+v", s)
	}
	// Boot and firewall changes are live; limits, devices and environment need a restart.
	want := []string{"devfs.t.device", "env.TZ", "network.eth0.ipv4", "network.eth0.mtu", "network.eth0.network", "network.eth1.network",
		"resources.cpu", "resources.memory", "volumes.data.mount", "volumes.data.read_only"}
	if !reflect.DeepEqual(restart, want) {
		t.Fatalf("restart:\n got %v\nwant %v", restart, want)
	}
	// Setting the same value again changes nothing and needs no restart.
	restart, err = ApplyConfig(&s, map[string]string{"resources.memory": "2G", "boot.priority": "5"}, nil)
	if err != nil || len(restart) != 0 || s.Boot.Priority != 5 {
		t.Fatalf("%v %v", err, restart)
	}
	if _, err := ApplyConfig(&s, map[string]string{"network.eth0.mtu": "1500"}, nil); err != nil || s.Network[0].MTU != 1500 {
		t.Fatalf("%v %+v", err, s.Network)
	}
	// there is one spelling of a device key: the path of the spec
	if _, err := ApplyConfig(&s, map[string]string{"nic.eth0.mtu": "1400"}, nil); err == nil {
		t.Fatal("nic.eth0.mtu is no key: the device keys are network.<name>.<field>")
	}
}

func TestApplyConfigUnsetAndDevices(t *testing.T) {
	s := JailSpec{Network: []NetworkAttach{{Network: "lan", IPv4: "auto"}, {Network: "dmz"}}, Resources: Resources{Memory: "1G"}, Volumes: []VolumeAttach{{Name: "data", Mount: "/d"}}}
	restart, err := ApplyConfig(&s, nil, []string{"resources.memory", "network.eth1", "volumes.data"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Resources.Memory != "" || len(s.Network) != 1 || s.Network[0].Name != "eth0" || len(s.Volumes) != 0 {
		t.Fatalf("%+v", s)
	}
	if !reflect.DeepEqual(restart, []string{"network.eth1", "resources.memory", "volumes.data"}) {
		t.Fatal(restart)
	}
	if _, err := ApplyConfig(&s, nil, []string{"network.eth7"}); err != nil {
		t.Fatalf("removing a device that is not there is not an error: %v", err)
	}
}

func TestApplyConfigRejectsAndIsAtomic(t *testing.T) {
	s := JailSpec{Resources: Resources{CPU: 1}}
	for name, set := range map[string]map[string]string{
		"unknown":        {"limits.ram": "1G"},
		"bad int":        {"resources.cpu": "many"},
		"range":          {"resources.cpu": "100000"},
		"bad size":       {"resources.memory": "lots"},
		"zero size":      {"resources.memory": "0"},
		"bad bool":       {"boot.autostart": "maybe"},
		"bad enum":       {"update.mode": "yolo"},
		"bad mtu":        {"network.eth0.mtu": "10"},
		"bad address":    {"network.eth0.ipv4": "fe80::1"},
		"bad v6":         {"network.eth0.ipv6": "10.0.0.1"},
		"bad mac":        {"network.eth0.mac": "AA:BB"},
		"device name":    {"nic.Bad_Name.network": "lan"},
		"devfs list":     {"devfs.x.device": "kmem"},
		"mount path":     {"volumes.d.mount": "../etc"},
		"old proxy key":  {"proxy.p.listen": ":80"},
		"env name":       {"env.1X": "y"},
		"rate":           {"network.eth0.egress": "fast"},
		"hostname":       {"hostname": "bad host!"},
		"list item":      {"boot.depends": "Bad Name"},
		"injection":      {"firewall.policies": "x\"; rm"},
		"profile loop":   {"profiles": "UPPER"},
		"partial values": {"resources.cpu": "2", "resources.memory": "zzz"},
	} {
		before := s
		if _, err := ApplyConfig(&s, set, nil); err == nil {
			t.Errorf("%s: accepted", name)
		} else if !IsCode(err, CodeInvalid) {
			t.Errorf("%s: %v", name, err)
		}
		if !reflect.DeepEqual(before, s) {
			t.Errorf("%s: the spec changed although the call failed: %+v", name, s)
		}
	}
	if _, err := ApplyConfig(&s, map[string]string{"resources.cpu": "2"}, []string{"nope"}); err == nil || s.Resources.CPU != 1 {
		t.Fatalf("%v %+v", err, s)
	}
}

func prof(name string, cfg map[string]string) Profile {
	return Profile{Metadata: ObjectMeta{Name: name}, Spec: ProfileSpec{Config: cfg}}
}

func TestExpandedConfigMergesInOrderAndReportsSources(t *testing.T) {
	jail := JailSpec{Image: "fb", Profiles: []string{"base", "web"},
		Resources: Resources{Memory: "4G"},
		Network:   []NetworkAttach{{Network: "lan", IPv4: "10.0.0.5", MTU: 1300}}}
	base := prof("base", map[string]string{"resources.memory": "1G", "resources.cpu": "1", "boot.autostart": "true", "network.eth0.mtu": "9000", "network.eth1.network": "mgmt", "env.TZ": "UTC"})
	web := prof("web", map[string]string{"resources.cpu": "4", "network.eth1.mtu": "1400", "volumes.data.mount": "/var/db"})
	got, src := ExpandedConfig(jail, []Profile{base, web})
	if got.Resources.Memory != "4G" || got.Resources.CPU != 4 || !got.Boot.Autostart || got.Env["TZ"] != "UTC" {
		t.Fatalf("%+v", got)
	}
	if len(got.Network) != 2 || got.Network[0].MTU != 1300 || got.Network[0].IPv4 != "10.0.0.5" || got.Network[1].Name != "eth1" ||
		got.Network[1].Network != "mgmt" || got.Network[1].MTU != 1400 {
		t.Fatalf("device by device: %+v", got.Network)
	}
	if len(got.Volumes) != 1 {
		t.Fatalf("%+v", got)
	}
	for k, want := range map[string]string{"resources.memory": "jail", "resources.cpu": "web", "boot.autostart": "base", "network.eth0.mtu": "jail",
		"network.eth1.network": "base", "network.eth1.mtu": "web", "env.TZ": "base", "image": "jail"} {
		if src[k] != want {
			t.Errorf("source of %s: %q, want %q", k, src[k], want)
		}
	}
	// The jail's own spec is untouched.
	if jail.Resources.CPU != 0 || len(jail.Network) != 1 {
		t.Fatalf("%+v", jail)
	}
	// Without profiles the spec comes back as it was.
	same, srcs := ExpandedConfig(jail, nil)
	if !reflect.DeepEqual(same.Resources, jail.Resources) || srcs["resources.memory"] != "jail" {
		t.Fatal(same, srcs)
	}
}

func TestProfilesAllowOnlyProfileKeys(t *testing.T) {
	if err := ValidateProfile(ProfileSpec{Config: map[string]string{"resources.memory": "1G", "network.eth1.network": "lan"}}); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"hostname", "image", "profiles", "network.eth0.ipv4", "network.eth0.mac"} {
		if err := ValidateProfile(ProfileSpec{Config: map[string]string{k: "x"}}); err == nil || !strings.Contains(err.Error(), k) {
			t.Errorf("%s allowed in a profile: %v", k, err)
		}
	}
	if err := ValidateProfile(ProfileSpec{Config: map[string]string{"resources.memory": "oops"}}); err == nil {
		t.Fatal("a bad value was accepted")
	}
}

func TestConfigValuesRoundTrip(t *testing.T) {
	var s JailSpec
	set := map[string]string{"resources.cpu": "2", "network.eth0.network": "lan", "network.eth0.ipv4": "auto", "volumes.d.mount": "/x", "env.A": "1"}
	if _, err := ApplyConfig(&s, set, nil); err != nil {
		t.Fatal(err)
	}
	for k, v := range set {
		if ConfigValues(s)[k] != v {
			t.Errorf("%s = %q", k, ConfigValues(s)[k])
		}
	}
	var t2 JailSpec
	if _, err := ApplyConfig(&t2, ConfigValues(s), nil); err != nil || !reflect.DeepEqual(ConfigValues(t2), ConfigValues(s)) {
		t.Fatalf("%v\n%v\n%v", err, ConfigValues(t2), ConfigValues(s))
	}
}

// zfs_delegation is a boolean of the jail itself: global administrators set it, and a profile cannot carry it.
func TestZFSDelegationKey(t *testing.T) {
	var s JailSpec
	restart, err := ApplyConfig(&s, map[string]string{"zfs_delegation": "true"}, nil)
	if err != nil || !s.Delegate || !reflect.DeepEqual(restart, []string{"zfs_delegation"}) {
		t.Fatalf("%v %v %+v", err, restart, s)
	}
	if v := ConfigValues(s); v["zfs_delegation"] != "true" {
		t.Fatalf("%v", v)
	}
	if _, err := ApplyConfig(&s, map[string]string{"zfs_delegation": "maybe"}, nil); err == nil || !s.Delegate {
		t.Fatalf("not a boolean: %v %+v", err, s)
	}
	if _, err := ApplyConfig(&s, nil, []string{"zfs_delegation"}); err != nil || s.Delegate {
		t.Fatalf("unset: %v %+v", err, s)
	}
	for _, k := range ConfigKeys() {
		if k.Name == "zfs_delegation" && k.Profile {
			t.Error("a profile may carry zfs_delegation")
		}
	}
}

func TestSysVIPCKey(t *testing.T) {
	var s JailSpec
	restart, err := ApplyConfig(&s, map[string]string{"sysvipc": "true"}, nil)
	if err != nil || !s.SysVIPC || !reflect.DeepEqual(restart, []string{"sysvipc"}) {
		t.Fatalf("%v %v %+v", err, restart, s)
	}
	if v := ConfigValues(s); v["sysvipc"] != "true" {
		t.Fatalf("%v", v)
	}
	if _, err := ApplyConfig(&s, map[string]string{"sysvipc": "inherit"}, nil); err == nil || !s.SysVIPC {
		t.Fatalf("not a boolean: %v %+v", err, s)
	}
	if _, err := ApplyConfig(&s, nil, []string{"sysvipc"}); err != nil || s.SysVIPC {
		t.Fatalf("unset: %v %+v", err, s)
	}
	// a profile may carry it
	eff, src := ExpandedConfig(JailSpec{}, []Profile{{Metadata: ObjectMeta{Name: "db"}, Spec: ProfileSpec{Config: map[string]string{"sysvipc": "true"}}}})
	if !eff.SysVIPC || src["sysvipc"] != "db" {
		t.Fatalf("%+v %v", eff, src)
	}
}

// boot.stop_timeout_seconds is the seconds a jail has to shut down by itself; it is written at the next start.
func TestStopTimeoutKey(t *testing.T) {
	var s JailSpec
	restart, err := ApplyConfig(&s, map[string]string{"boot.stop_timeout_seconds": "60"}, nil)
	if err != nil || s.Boot.StopTimeoutSeconds != 60 || !reflect.DeepEqual(restart, []string{"boot.stop_timeout_seconds"}) {
		t.Fatalf("%v %v %+v", err, restart, s.Boot)
	}
	if v := ConfigValues(s); v["boot.stop_timeout_seconds"] != "60" {
		t.Fatalf("%v", v)
	}
	var t2 JailSpec
	if _, err := ApplyConfig(&t2, ConfigValues(s), nil); err != nil || t2.Boot.StopTimeoutSeconds != 60 {
		t.Fatalf("round trip: %v %+v", err, t2.Boot)
	}
	for _, bad := range []string{"-1", "3601", "slow"} {
		if _, err := ApplyConfig(&JailSpec{}, map[string]string{"boot.stop_timeout_seconds": bad}, nil); err == nil {
			t.Errorf("%q is accepted", bad)
		}
	}
	if _, err := ApplyConfig(&s, nil, []string{"boot.stop_timeout_seconds"}); err != nil || s.Boot.StopTimeoutSeconds != 0 {
		t.Fatalf("unset: %v %+v", err, s.Boot)
	}
}

// Add and Remove change one entry of a list key and leave the others where they are.
func TestApplyChangeAddsAndRemovesListEntries(t *testing.T) {
	s := JailSpec{Firewall: FirewallAttach{Policies: []string{"ssh-office"}}}
	if _, err := ApplyChange(&s, ConfigChange{Add: map[string]string{"firewall.policies": "web-public"}}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(s.Firewall.Policies, ","); got != "ssh-office,web-public" {
		t.Fatalf("after an add: %s", got)
	}
	// an entry that is there already is not added twice
	if _, err := ApplyChange(&s, ConfigChange{Add: map[string]string{"firewall.policies": "ssh-office,monitoring"}}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(s.Firewall.Policies, ","); got != "ssh-office,web-public,monitoring" {
		t.Fatalf("after a second add: %s", got)
	}
	if _, err := ApplyChange(&s, ConfigChange{Remove: map[string]string{"firewall.policies": "web-public,nothing"}}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(s.Firewall.Policies, ","); got != "ssh-office,monitoring" {
		t.Fatalf("after a remove: %s", got)
	}
	if _, err := ApplyChange(&s, ConfigChange{Remove: map[string]string{"firewall.policies": "ssh-office,monitoring"}}); err != nil || len(s.Firewall.Policies) != 0 {
		t.Fatalf("the last entries leave an empty list: %v %v", err, s.Firewall.Policies)
	}
	// the entries are checked like the whole list, and a bad change leaves the spec as it was
	s.Firewall.Policies = []string{"a"}
	if _, err := ApplyChange(&s, ConfigChange{Add: map[string]string{"firewall.policies": "Bad Name"}}); err == nil || strings.Join(s.Firewall.Policies, ",") != "a" {
		t.Fatalf("a bad name: %v %v", err, s.Firewall.Policies)
	}
	// a key that is not a list, an unknown key and a key named twice are refused
	for _, ch := range []ConfigChange{
		{Add: map[string]string{"resources.memory": "2G"}},
		{Remove: map[string]string{"nope": "x"}},
		{Add: map[string]string{"firewall.policies": "b"}, Remove: map[string]string{"firewall.policies": "a"}},
		{Add: map[string]string{"firewall.policies": "b"}, Set: map[string]string{"firewall.policies": "c"}},
	} {
		if _, err := ApplyChange(&s, ch); err == nil {
			t.Errorf("%+v was accepted", ch)
		}
	}
	// without entries to add or remove it is ApplyConfig
	if _, err := ApplyChange(&s, ConfigChange{Set: map[string]string{"resources.cpu": "2"}}); err != nil || s.Resources.CPU != 2 {
		t.Fatalf("a plain change: %v", err)
	}
}
