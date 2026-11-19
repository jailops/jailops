package lifecycle

import (
	"encoding/json"
	"testing"

	"jailops.net/jailops/sdk/api"
)

// The proxy devices of a spec.json written before the exposure of jails come back as IPv4 expose entries.
func TestUpgradeSpecTurnsProxyDevicesIntoExposeEntries(t *testing.T) {
	old := []byte(`{"version":1,"metadata":{"name":"web","project":"alpha"},"spec":{"image":"fb","proxy":[` +
		`{"name":"http","listen":":8080","jail_port":80},` +
		`{"name":"dns","listen":"192.0.2.7:53","jail_port":53,"proto":"udp"},` +
		`{"name":"same","listen":":443","jail_port":443}]}}`)
	var r Record
	if err := json.Unmarshal(upgradeSpec(old), &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Spec.Expose) != 3 {
		t.Fatalf("%+v", r.Spec.Expose)
	}
	http, dns, same := r.Spec.Expose[0], r.Spec.Expose[1], r.Spec.Expose[2]
	if http.Protocol() != "tcp" || http.Ports[0] != "80" || http.HostPort != 8080 || len(http.Families) != 1 || http.Families[0] != "4" || http.Listen != "" {
		t.Errorf("http: %+v", http)
	}
	if dns.Protocol() != "udp" || dns.Ports[0] != "53" || dns.HostPort != 0 || dns.Listen != "192.0.2.7" {
		t.Errorf("dns: %+v", dns)
	}
	if same.HostPort != 0 || same.Ports[0] != "443" {
		t.Errorf("a proxy whose ports are equal has no host port: %+v", same)
	}
	if r.Spec.Image != "fb" || r.Metadata.Name != "web" {
		t.Errorf("the rest of the record changed: %+v", r)
	}
	// a record without proxy devices comes back as it is
	plain := []byte(`{"version":1,"spec":{"image":"fb","expose":[{"proto":"tcp","ports":["80"]}]}}`)
	if string(upgradeSpec(plain)) != string(plain) {
		t.Errorf("a current record was rewritten: %s", upgradeSpec(plain))
	}
	var _ api.ExposeEntry
}

// The alias of an image reference written before image aliases were removed is dropped when the record is read: every
// upgrade, update and image listing resolves "name" or "name#gN", and a jail whose reference does not parse would fall
// out of the image upgrades without a word.
func TestDecodeRecordDropsTheAliasOfTheImageReference(t *testing.T) {
	for in, want := range map[string]string{
		"freebsd-15-1@stable": "freebsd-15-1",
		"freebsd-15-1":        "freebsd-15-1",
		"freebsd-15-1#g2":     "freebsd-15-1#g2",
	} {
		raw := []byte(`{"version":1,"metadata":{"name":"web","project":"shop"},"spec":{"image":"` + in + `","storage":"default"}}`)
		r, err := DecodeRecord(raw)
		if err != nil || r.Spec.Image != want || r.Spec.Storage != "default" || r.Metadata.Name != "web" {
			t.Errorf("%q: %v %+v", in, err, r)
		}
		b, err := DecodeBundle([]byte(`{"version":1,"metadata":{"name":"web"},"spec":{"image":"` + in + `"},"image":{"ref":"x","name":"x#g1","generation":1}}`))
		if err != nil || b.Spec.Image != want || b.Image.Generation != 1 {
			t.Errorf("bundle %q: %v %+v", in, err, b)
		}
	}
}
