package service

import (
	"strings"
	"testing"
	"time"

	"jailops.net/jailops/sdk/api"
)

func TestDoctorNDReplyPath(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	run := func(st api.NDPStatus) []api.DoctorCheck {
		c := &checkSet{}
		ndReplyPathCheck(c, st, now)
		return c.list
	}
	// no capture on this host: no line
	if got := run(api.NDPStatus{}); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
	up := []api.NDPUplink{{Uplink: "vtnet0", Open: true}}
	if got := run(api.NDPStatus{Uplinks: up}); len(got) != 1 || got[0].Name != "network.nd_reply_path" || got[0].Status != api.DoctorInfo || !strings.Contains(got[0].Message, "no data yet") {
		t.Fatalf("%+v", got)
	}
	up[0].HostSolicits = 12
	good := api.NDPReplyPath{Uplink: "vtnet0", Router: "3fff:40::2", HostAddress: "3fff:40:12c::1", Repeats: 1, LastSeen: now.Add(-time.Hour),
		RouterMAC: "02:00:00:00:00:02", NADstMAC: "02:00:00:00:00:02"}
	if got := run(api.NDPStatus{Uplinks: up, ReplyPath: []api.NDPReplyPath{good}}); len(got) != 1 || got[0].Status != api.DoctorOK {
		t.Fatalf("%+v", got)
	}
	bad := api.NDPReplyPath{Uplink: "vtnet0", Router: "3fff:40::3", HostAddress: "3fff:40:12c::1", Repeats: 4, LastSeen: now.Add(-time.Hour),
		RouterMAC: "02:00:00:00:00:03", NADstMAC: "00:00:5e:00:02:01", Misdirected: true}
	got := run(api.NDPStatus{Uplinks: up, ReplyPath: []api.NDPReplyPath{good, bad}})
	if len(got) != 1 || got[0].Status != api.DoctorWarn || !strings.Contains(got[0].Message, "router 3fff:40::3") ||
		!strings.Contains(got[0].Message, "00:00:5e:00:02:01") || strings.Contains(got[0].Message, "3fff:40::2 ") ||
		!strings.Contains(got[0].Hint, `ipv6_static_routes="infra"`) || !strings.Contains(got[0].Hint, `ipv6_route_infra="-net 3fff:40::/64 -interface vtnet0"`) ||
		!strings.Contains(got[0].Hint, "docs/networking-ipv6.md") {
		t.Fatalf("%+v", got)
	}
	// a day later it is forgotten
	bad.LastSeen = now.Add(-25 * time.Hour)
	if got := run(api.NDPStatus{Uplinks: up, ReplyPath: []api.NDPReplyPath{bad}}); len(got) != 1 || got[0].Status != api.DoctorOK {
		t.Fatalf("%+v", got)
	}
}
