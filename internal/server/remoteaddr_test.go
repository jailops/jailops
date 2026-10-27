package server

import (
	"net/http/httptest"
	"testing"

	"jailops.net/jailops/internal/auth"
)

// Local users are told apart by uid, so the failed-login limiter and the audit log treat them separately and one
// local user cannot lock the others out of login.
func TestRemoteAddrSeparatesLocalUsers(t *testing.T) {
	cases := []struct {
		name string
		addr string
		cred *auth.Cred
		want string
	}{
		{"local user 1000", "@", &auth.Cred{UID: 1000}, "unix:1000"},
		{"local user 1001", "@", &auth.Cred{UID: 1001}, "unix:1001"},
		{"root", "@", &auth.Cred{UID: 0}, "unix:0"},
		{"socket without credentials", "@", nil, "unix"},
		{"tcp peer", "192.0.2.7:51234", nil, "192.0.2.7"},
		{"tcp peer v6", "[2001:db8::7]:51234", nil, "2001:db8::7"},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/api/v1", nil)
		r.RemoteAddr = c.addr
		if c.cred != nil {
			r = r.WithContext(WithPeerCred(r.Context(), *c.cred))
		}
		if got, _ := remoteAddr(r); got != c.want {
			t.Errorf("%s: remoteAddr = %q, want %q", c.name, got, c.want)
		}
	}
}
