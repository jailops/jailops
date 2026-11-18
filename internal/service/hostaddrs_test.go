package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"jailops.net/jailops/internal/sys/ifconfig"
	"jailops.net/jailops/sdk/api"
)

type failingNet struct{ ifconfig.Client }

func (failingNet) List(context.Context) ([]ifconfig.Iface, error) {
	return nil, errors.New("ifconfig: cannot list")
}

// The table <host> is what closes the host to a jail: an anchor rendered from a table that is empty because the interfaces
// could not be listed would not close it, so the reading is an error, not an empty list.
func TestHostAddressesThatCannotBeReadAreAnError(t *testing.T) {
	s := &Service{now: time.Now}
	s.d.Net = failingNet{}
	if a, err := s.hostAddrsChecked(context.Background()); err == nil {
		t.Fatalf("an unreadable host gave %v and no error", a)
	}
	// a host without a network reader (a test host with no FreeBSD under it) has nothing to read: no error
	s2 := &Service{now: time.Now}
	if _, err := s2.hostAddrsChecked(context.Background()); err != nil {
		t.Fatalf("no reader: %v", err)
	}
}

// The anchor of a WireGuard server closes the host to the peers with the same table: a host whose addresses cannot be
// read gets a Problem and no ruleset, so the tunnel stays as it was, and a readable one gets the anchor with the
// networks the tunnel serves as the only destinations a peer reaches.
func TestWireGuardAnchorIsFailClosedLikeTheJailAnchors(t *testing.T) {
	o := object[api.WireGuardSpec]{Meta: api.ObjectMeta{Name: "office"}, Spec: api.WireGuardSpec{
		Mode: api.WireGuardServer, Interface: "wg-office", Address: []string{"10.99.0.1/24"}, ListenPort: 51820}}
	s := &Service{now: time.Now}
	s.d.Net = failingNet{}
	d := s.wgDesired(o)
	if d.Problem == "" || d.Ruleset != "" {
		t.Fatalf("an unreadable host gave problem %q and a ruleset of %d bytes", d.Problem, len(d.Ruleset))
	}
	s.d.Net = nil
	s = &Service{now: time.Now}
	d = s.wgDesired(o)
	if d.Problem != "" || !strings.Contains(d.Ruleset, "block drop in quick on wg-office all") {
		t.Fatalf("a tunnel with no network: problem %q\n%s", d.Problem, d.Ruleset)
	}
}

// A thin jail that was stored with the update strategy inplace (accepted before the rule) is a warning of doctor, not an
// error that stops its updates.
func TestDoctorWarnsAboutThinJailsWithInPlace(t *testing.T) {
	s := &Service{now: time.Now, jails: map[string]*jailEntry{
		"alpha_old": {jail: api.Jail{Metadata: api.ObjectMeta{Name: "old", Project: "alpha"},
			Spec: api.JailSpec{OS: api.OSFreeBSD, Layout: api.LayoutThin, Update: api.UpdatePolicy{Strategy: api.UpdateInPlace}}}},
		"alpha_ok": {jail: api.Jail{Metadata: api.ObjectMeta{Name: "ok", Project: "alpha"},
			Spec: api.JailSpec{OS: api.OSFreeBSD, Layout: api.LayoutThick, Update: api.UpdatePolicy{Strategy: api.UpdateInPlace}}}},
	}}
	c := &checkSet{}
	s.doctorInPlaceThinJails(c)
	if len(c.list) != 1 || c.list[0].Status != api.DoctorWarn || !strings.Contains(c.list[0].Message, "alpha/old") || strings.Contains(c.list[0].Message, "alpha/ok") {
		t.Fatalf("%+v", c.list)
	}
}
