package cli

import (
	"testing"

	"jailops.net/jailops/sdk/api"
)

// The EXPOSED cell is "PORTS/PROTO FAMILIES": groups of the same ports and protocol share their families, a star marks a
// restricted source, a forward whose host port differs reads HOST→JAIL, and a cell that does not fit ends with "+N".
func TestExposedCellForms(t *testing.T) {
	e := func(proto, ports, fam, via string, host int, restricted bool) api.ExposedEntry {
		return api.ExposedEntry{Proto: proto, Ports: ports, Family: fam, Via: via, HostPort: host, Restricted: restricted}
	}
	for _, c := range []struct {
		name string
		in   []api.ExposedEntry
		want string
	}{
		{"nothing", nil, "-"},
		{"both families share a group", []api.ExposedEntry{e("tcp", "80,443", "6", "uplink", 0, false), e("tcp", "80,443", "4", "forward", 0, false)}, "80,443/tcp 4,6"},
		{"restricted", []api.ExposedEntry{e("tcp", "22", "6", "uplink", 0, true)}, "22/tcp 6*"},
		{"another host port", []api.ExposedEntry{e("tcp", "80", "4", "forward", 8080, false)}, "8080→80/tcp 4"},
		{"the host port is the jail's port", []api.ExposedEntry{e("tcp", "80", "4", "forward", 80, false)}, "80/tcp 4"},
		{"too long ends with +N", []api.ExposedEntry{e("tcp", "25,465,587,993", "4", "forward", 0, false), e("udp", "53", "4", "forward", 0, false), e("tcp", "22", "6", "uplink", 0, true)}, "25,465,587,993/tcp 4 +2"},
	} {
		if got := exposedText(c.in); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}
