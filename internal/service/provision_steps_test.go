package service

import (
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
)

// The packages of a provision are installed with a plain pkg install (FreeBSD) or apk add (Linux), and a service is
// enabled by an edit of rc.conf made by the host: the minimal base set has no sysrc(8).
func TestProvisionSteps(t *testing.T) {
	p := api.Provision{Packages: []string{"postgresql17-server"}, Services: api.ProvisionSvc{Enable: []string{"postgresql"}}}
	cmd := func(linux bool) string {
		for _, st := range provisionSteps(p, linux) {
			if st.name == "packages" {
				return strings.Join(st.argv, " ")
			}
		}
		return ""
	}
	if got := cmd(false); got != "pkg install -y postgresql17-server" {
		t.Errorf("freebsd: %q", got)
	}
	if got := cmd(true); !strings.HasPrefix(got, "apk add") {
		t.Errorf("linux: %q", got)
	}
	for _, st := range provisionSteps(p, false) {
		if st.name == "enable postgresql" && (st.rc == nil || st.rc.Name != "postgresql_enable" || st.rc.Value != "YES") {
			t.Errorf("enable step: %+v", st)
		}
	}
}
