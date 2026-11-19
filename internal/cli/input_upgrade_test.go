package cli

import (
	"testing"

	"jailops.net/jailops/sdk/api"
)

// A file an administrator wrote for an earlier release is read as the current shapes (a renamed or removed field is
// converted), and a key that never was valid is still an error.
func TestAFileOfAnEarlierReleaseIsReadByTheStrictParser(t *testing.T) {
	docs, err := readDocs([]byte(`apiVersion: jailops/v1
kind: Network
metadata: {name: lan}
spec:
  ipv4: {subnet: 10.0.0.0/24, gateway: 10.0.0.1, range: 10.0.0.2-10.0.0.9}
  ipv6: {mode: proxy, subnet: "2001:db8:1::/64", range: "2001:db8:1::10-2001:db8:1::ff"}
`))
	if err != nil {
		t.Fatal(err)
	}
	_, spec, err := parseObject[api.NetworkSpec](docs[0], api.KindNetwork, "")
	if err != nil {
		t.Fatalf("a network with ipv6.mode: %v", err)
	}
	if spec.IPv6 == nil || !spec.IPv6.NDPProxy {
		t.Errorf("ipv6: %+v", spec.IPv6)
	}
	// a bare spec of a jail with a proxy device and an alias
	bare, err := readDocs([]byte("image: freebsd-15-1@stable\nproxy:\n  - {name: http, listen: ':8080', jail_port: 80}\n"))
	if err != nil {
		t.Fatal(err)
	}
	_, js, err := parseObject[api.JailSpec](bare[0], api.KindJail, "web")
	if err != nil {
		t.Fatalf("a jail with a proxy device: %v", err)
	}
	if js.Image != "freebsd-15-1" || len(js.Expose) != 1 || js.Expose[0].HostPort != 8080 {
		t.Errorf("jail: image %q expose %+v", js.Image, js.Expose)
	}
	misspelt, _ := readDocs([]byte("image: freebsd-15-1\nimagee: x\n"))
	if _, _, err := parseObject[api.JailSpec](misspelt[0], api.KindJail, "web"); err == nil {
		t.Error("a misspelt key was accepted")
	}
}
