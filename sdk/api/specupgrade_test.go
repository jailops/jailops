package api

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func doc(t *testing.T, s string) map[string]any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestUpgradeSpecRewritesTheShapesOfEarlierReleases(t *testing.T) {
	cases := []struct {
		name  string
		kind  Kind
		in    string
		want  string
		notes int
	}{
		{"network proxy mode", KindNetwork, `{"ipv6":{"mode":"proxy","subnet":"2001:db8::/64"}}`, `{"ipv6":{"ndp_proxy":true,"subnet":"2001:db8::/64"}}`, 1},
		{"network routed mode", KindNetwork, `{"ipv6":{"mode":"routed","subnet":"2001:db8::/64"}}`, `{"ipv6":{"subnet":"2001:db8::/64"}}`, 1},
		{"network no ipv6", KindNetwork, `{"ipv4":{"subnet":"10.0.0.0/24"},"ipv6":{"mode":"none"}}`, `{"ipv4":{"subnet":"10.0.0.0/24"}}`, 1},
		{"tunnel down", KindWireGuard, `{"mode":"server","down":true}`, `{"mode":"server","disabled":true}`, 1},
		{"tunnel with both keeps the new one", KindWireGuard, `{"down":true,"disabled":false}`, `{"disabled":false}`, 1},
		{"image aliases", KindImage, `{"release":"15.1-RELEASE","aliases":["stable"]}`, `{"release":"15.1-RELEASE"}`, 1},
		{"jail image alias", KindJail, `{"image":"freebsd-15-1@stable"}`, `{"image":"freebsd-15-1"}`, 1},
		{"project image alias", KindProject, `{"images":["freebsd-15-1@stable","alpine-3"]}`, `{"images":["freebsd-15-1","alpine-3"]}`, 1},
		{"jail proxy device", KindJail, `{"image":"x","proxy":[{"name":"http","listen":"0.0.0.0:8080","jail_port":80}]}`,
			`{"image":"x","expose":[{"proto":"tcp","ports":["80"],"families":["4"],"comment":"migrated from proxy http","host_port":8080,"listen":"0.0.0.0"}]}`, 1},
		{"jail proxy device with the same port and an expose already", KindJail,
			`{"expose":[{"proto":"tcp","ports":["22"]}],"proxy":[{"name":"dns","listen":":53","jail_port":53,"proto":"udp"}]}`,
			`{"expose":[{"proto":"tcp","ports":["22"]},{"proto":"udp","ports":["53"],"families":["4"],"comment":"migrated from proxy dns"}]}`, 1},
		{"a current network", KindNetwork, `{"ipv6":{"ndp_proxy":true,"subnet":"2001:db8::/64"}}`, `{"ipv6":{"ndp_proxy":true,"subnet":"2001:db8::/64"}}`, 0},
		{"a current jail", KindJail, `{"image":"freebsd-15-1#g2","expose":[{"proto":"tcp","ports":["80"]}]}`, `{"image":"freebsd-15-1#g2","expose":[{"proto":"tcp","ports":["80"]}]}`, 0},
	}
	for _, c := range cases {
		spec := doc(t, c.in)
		notes := UpgradeSpec(c.kind, spec)
		if len(notes) != c.notes {
			t.Errorf("%s: notes %q, want %d", c.name, notes, c.notes)
		}
		got, _ := json.Marshal(spec)
		want, _ := json.Marshal(doc(t, c.want))
		if string(got) != string(want) {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, want)
		}
		// a second pass finds nothing left to rewrite
		if again := UpgradeSpec(c.kind, spec); len(again) != 0 {
			t.Errorf("%s: a second pass rewrote %q", c.name, again)
		}
	}
}

// YAML gives plain ints, JSON gives numbers: the port of a proxy device is read either way.
func TestUpgradeSpecReadsYAMLIntegers(t *testing.T) {
	spec := map[string]any{"proxy": []any{map[string]any{"name": "http", "listen": ":8080", "jail_port": 80}}}
	if notes := UpgradeSpec(KindJail, spec); len(notes) != 1 {
		t.Fatalf("notes %q", notes)
	}
	e, _ := spec["expose"].([]any)
	if len(e) != 1 || e[0].(map[string]any)["host_port"] != 8080 {
		t.Errorf("expose %+v", spec["expose"])
	}
}

func TestUpgradeFieldsRenamesTheNamesOfTheFreeze(t *testing.T) {
	cases := []struct {
		name string
		kind Kind
		in   string
		want string
	}{
		{"stop timeout", KindJail, `{"boot":{"autostart":true,"stop_timeout":60}}`, `{"boot":{"autostart":true,"stop_timeout_seconds":60}}`},
		{"strategy manual is mode manual", KindJail, `{"update":{"strategy":"manual","window":"sun 03:00"}}`, `{"update":{"mode":"manual"}}`},
		{"strategy manual keeps a scheduled mode", KindJail, `{"update":{"strategy":"manual","mode":"notify"}}`, `{"update":{"mode":"notify"}}`},
		{"strategy rebase is gone", KindJail, `{"update":{"strategy":"rebase","healthcheck":"/bin/true"}}`, `{"update":{"healthcheck":"/bin/true"}}`},
		{"expose by and at", KindJail, `{"expose":[{"ports":["80"],"by":"admin","at":"2026-01-02T03:04:05Z"}]}`, `{"expose":[{"ports":["80"]}]}`},
		{"profile keys", KindProfile,
			`{"config":{"limits.memory":"1G","nic.eth1.network":"lan","disk.data.mount":"/d","updates.mode":"managed","zfs.delegation":"true","boot.stop_timeout":"9","update.strategy":"rebase","update.window":"x","hostname":"a"}}`,
			`{"config":{"resources.memory":"1G","network.eth1.network":"lan","volumes.data.mount":"/d","update.mode":"managed","zfs_delegation":"true","boot.stop_timeout_seconds":"9","hostname":"a"}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := doc(t, c.in)
			if notes := UpgradeFields(c.kind, got); len(notes) == 0 {
				t.Error("no note")
			}
			want := doc(t, c.want)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("got %v\nwant %v", got, want)
			}
			// a second pass changes nothing and says nothing
			if notes := UpgradeFields(c.kind, got); len(notes) != 0 || !reflect.DeepEqual(got, want) {
				t.Errorf("second pass: %v %v", notes, got)
			}
		})
	}
}

func TestEveryConfigKeyIsThePathOfTheSpec(t *testing.T) {
	for _, k := range ConfigKeys() {
		want := strings.NewReplacer("[<name>]", ".<name>").Replace(k.Path)
		if k.Name != want {
			t.Errorf("key %s has the path %s", k.Name, k.Path)
		}
	}
}
