package service

import (
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
)

func TestOnlinkNSVerdict(t *testing.T) {
	for _, c := range []struct {
		name    string
		got     string
		proxy   bool
		n       uint64
		counted bool
		want    api.DoctorStatus
		text    string
	}{
		{"default, proxy network, nothing rejected", "0", true, 0, true, api.DoctorInfo, "the default"},
		{"default, proxy network, counter unreadable", "0", true, 0, false, api.DoctorInfo, "the default"},
		{"default, proxy network, solicitations rejected", "0", true, 104, true, api.DoctorWarn, "rejected 104 neighbor solicitations"},
		{"set to 1 and solicitations were rejected earlier", "1", true, 104, true, api.DoctorOK, "=1"},
		{"default, no proxy network, solicitations rejected", "0", false, 104, true, api.DoctorOK, "not needed now"},
	} {
		st, msg, hint := onlinkNSVerdict(c.got, c.proxy, c.n, c.counted)
		if st != c.want || !strings.Contains(msg, c.text) {
			t.Errorf("%s: %s %q", c.name, st, msg)
		}
		if st == api.DoctorWarn && !strings.Contains(hint, "/etc/sysctl.conf") {
			t.Errorf("%s: the hint does not say where to keep it: %q", c.name, hint)
		}
	}
}

func TestRejectedNSIsReadFromNetstat(t *testing.T) {
	const out = `icmp6:
	0 calls to icmp_error
	0 messages with bad code fields
	0 messages with bad length
	0 messages with bad ND options
	104 bad neighbor solicitation messages
	0 bad neighbor advertisement messages
	0 bad router solicitation messages
`
	if n, ok := parseRejectedNS(out); !ok || n != 104 {
		t.Fatalf("%d %v", n, ok)
	}
	if _, ok := parseRejectedNS("icmp6:\n\t0 bad neighbor advertisement messages\n"); ok {
		t.Fatal("a line about advertisements was taken for the solicitations")
	}
	if _, ok := parseRejectedNS(""); ok {
		t.Fatal("an empty output was taken for a count")
	}
}
