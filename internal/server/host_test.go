package server_test

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"jailops.net/jailops/internal/daemon"
	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/sdk/api"
)

func hostSpec() api.HostIdentitySpec {
	return api.HostIdentitySpec{Hostname: "web1.example.org",
		Resolvers: &api.HostResolvers{Nameservers: []string{"192.0.2.53", "2001:db8::53"}, Search: []string{"example.org"}},
		NTP:       &api.HostNTP{Servers: []string{"192.0.2.123"}, Enable: true}}
}

func doctorCheck(t *testing.T, e *dtest.Env, name string) api.DoctorCheck {
	t.Helper()
	r, err := e.Admin.Doctor(ctxT(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range r.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("doctor has no check %s", name)
	return api.DoctorCheck{}
}

// Off by default: reading works for any global identity, changing answers not_managed with the lines to add, and
// the host is untouched.
func TestHostIdentityNotManaged(t *testing.T) {
	e := dtest.New(t, dtest.Options{})
	c := ctxT(t)
	info, err := e.Admin.Info(c)
	if err != nil || !hasStr(info.APIExtensions, "host_identity") {
		t.Fatalf("extension: %v %v", err, info.APIExtensions)
	}
	id, err := e.Client(api.PermReadonly).HostSystem().Identity().Get(c)
	if err != nil || id.Spec.Hostname != "old.example.org" || id.Status.Managed || id.Status.ConfigEnabled || id.Status.HelperEnabled ||
		!slices.Equal(id.Spec.Resolvers.Nameservers, []string{"198.51.100.1"}) {
		t.Fatalf("%+v %v", id, err)
	}
	_, err = e.Admin.HostSystem().Identity().Apply(c, hostSpec(), true)
	wantCode(t, err, api.CodeNotManaged)
	var ae *api.Error
	if !asErr(err, &ae) || !strings.Contains(ae.Hint, "jailopsd.yaml") || !strings.Contains(ae.Hint, "host-manage = identity") {
		t.Errorf("hint: %v", err)
	}
	_, err = e.Admin.HostSystem().Restore(c, "latest", false)
	wantCode(t, err, api.CodeNotManaged)
	if e.Host.File("resolv.conf") != dtest.HostResolv || e.Host.Hostname() != "old.example.org" {
		t.Error("the host changed")
	}
	if ck := doctorCheck(t, e, "host.identity"); ck.Status != api.DoctorOK || !strings.Contains(ck.Message, "not managed") {
		t.Errorf("%+v", ck)
	}
	if ck := doctorCheck(t, e, "host.ntp"); ck.Status != api.DoctorWarn {
		t.Errorf("ntpd is not enabled: %+v", ck)
	}

	// jailopsd.yaml alone is not enough: the helper refuses, and the hint names priv.conf only
	e2 := dtest.New(t, dtest.Options{Mutate: func(cfg *daemon.Config) { cfg.Host.Manage.Modules = []string{"identity"} }})
	_, err = e2.Admin.HostSystem().Identity().Apply(c, hostSpec(), false)
	wantCode(t, err, api.CodeNotManaged)
	if !asErr(err, &ae) || strings.Contains(ae.Hint, "jailopsd.yaml") || !strings.Contains(ae.Hint, "priv.conf") {
		t.Errorf("hint: %v", err)
	}
	if ck := doctorCheck(t, e2, "host.identity"); ck.Status != api.DoctorWarn {
		t.Errorf("enabled on one side only: %+v", ck)
	}
	// and priv.conf alone neither
	e3 := dtest.New(t, dtest.Options{HostManage: []string{"identity"}})
	_, err = e3.Admin.HostSystem().Identity().Apply(c, hostSpec(), false)
	wantCode(t, err, api.CodeNotManaged)
	if !asErr(err, &ae) || !strings.Contains(ae.Hint, "jailopsd.yaml") || strings.Contains(ae.Hint, "priv.conf") {
		t.Errorf("hint: %v", err)
	}
	if e2.Host.File("resolv.conf") != dtest.HostResolv || e3.Host.File("resolv.conf") != dtest.HostResolv {
		t.Error("the host changed")
	}
}

func TestHostIdentityManaged(t *testing.T) {
	e := dtest.New(t, dtest.Options{HostManage: []string{"identity"},
		Mutate: func(cfg *daemon.Config) { cfg.Host.Manage.Modules = []string{"identity"} }})
	c := ctxT(t)
	// an operator may not, a project admin may not
	_, err := e.Client(api.PermOperator).HostSystem().Identity().Apply(c, hostSpec(), true)
	wantCode(t, err, api.CodeForbidden)
	_, err = e.Client(api.PermReadonly).HostSystem().Restore(c, "latest", true)
	wantCode(t, err, api.CodeForbidden)
	_, err = e.Admin.HostSystem().Identity().Apply(c, api.HostIdentitySpec{Hostname: "web1\nsshd_enable=NO"}, true)
	wantCode(t, err, api.CodeInvalid)

	h := e.Admin.HostSystem()
	dry, err := h.Identity().Apply(c, hostSpec(), true)
	if err != nil || !dry.DryRun || !strings.Contains(dry.Diff, "+nameserver 2001:db8::53") || len(dry.Actions) == 0 {
		t.Fatalf("%+v %v", dry, err)
	}
	if e.Host.File("resolv.conf") != dtest.HostResolv {
		t.Fatal("the dry run changed the host")
	}
	res, err := h.Identity().Apply(c, hostSpec(), false)
	if err != nil || !res.Changed || res.Snapshot == "" {
		t.Fatalf("%+v %v", res, err)
	}
	if !strings.HasPrefix(e.Host.File("resolv.conf"), "# managed by jailops\n") || !strings.Contains(e.Host.File("ntp.conf"), "server 192.0.2.123 iburst") ||
		!strings.Contains(e.Host.File("rc.conf"), `hostname="web1.example.org"`) || e.Host.Hostname() != "web1.example.org" || e.Host.NTPRestarts() != 1 {
		t.Fatalf("%q %q %q", e.Host.File("resolv.conf"), e.Host.File("rc.conf"), e.Host.Hostname())
	}
	id, err := h.Identity().Get(c)
	if err != nil || !id.Status.Managed || len(id.Status.Drift) != 0 || id.Status.Applied == nil || id.Status.AppliedAt == nil || !id.Spec.NTP.Enable {
		t.Fatalf("%+v %v", id, err)
	}
	if ck := doctorCheck(t, e, "host.identity"); ck.Status != api.DoctorOK {
		t.Errorf("%+v", ck)
	}
	if ck := doctorCheck(t, e, "host.ntp"); ck.Status != api.DoctorOK || !strings.Contains(ck.Message, "192.0.2.123") {
		t.Errorf("%+v", ck)
	}
	e.Host.SetNTPQ("     remote   refid\n=====\n 192.0.2.123 .INIT. 16 u - 64 0 0.000 0.000 0.000\n")
	if ck := doctorCheck(t, e, "host.ntp"); ck.Status != api.DoctorWarn {
		t.Errorf("not synchronised: %+v", ck)
	}

	// a hand edit is drift, reported and not overwritten
	mustWriteFile(t, e.Host.Etc+"/resolv.conf", "nameserver 198.51.100.7\n")
	id, _ = h.Identity().Get(c)
	if !slices.Equal(id.Status.Drift, []string{"resolvers"}) {
		t.Errorf("drift %v", id.Status.Drift)
	}
	if ck := doctorCheck(t, e, "host.identity"); ck.Status != api.DoctorWarn || !strings.Contains(ck.Message, "resolvers") {
		t.Errorf("%+v", ck)
	}
	if e.Host.File("resolv.conf") != "nameserver 198.51.100.7\n" {
		t.Error("the hand edit was overwritten")
	}

	// back to the first snapshot
	snaps, err := h.Snapshots(c)
	if err != nil || len(snaps) != 1 || snaps[0].Name != res.Snapshot {
		t.Fatalf("%+v %v", snaps, err)
	}
	back, err := h.Restore(c, res.Snapshot, false)
	if err != nil || !back.Changed || back.Snapshot == "" {
		t.Fatalf("%+v %v", back, err)
	}
	if e.Host.File("resolv.conf") != dtest.HostResolv || e.Host.File("ntp.conf") != dtest.HostNTP || e.Host.File("rc.conf") != dtest.HostRC || e.Host.Hostname() != "old.example.org" {
		t.Error("the restore did not put the host back")
	}
	if id, _ := h.Identity().Get(c); id.Status.Applied != nil || len(id.Status.Drift) != 0 {
		t.Errorf("%+v", id.Status)
	}
	// every change is audited, dry runs too
	entries, err := e.Admin.Audit(c, 50)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, a := range entries {
		if strings.Contains(a.Action, "/api/v1/host/") {
			got = append(got, a.Action+" "+a.Result+" "+a.Detail)
		}
	}
	all := strings.Join(got, "\n")
	for _, want := range []string{"PUT /api/v1/host/identity ok dry_run", "PUT /api/v1/host/identity ok snapshot=" + res.Snapshot,
		"PUT /api/v1/host/identity denied", "POST /api/v1/host/snapshots/{name}/restore ok snapshot=" + back.Snapshot} {
		if !strings.Contains(all, want) {
			t.Errorf("the audit log lacks %q:\n%s", want, all)
		}
	}
}

func asErr(err error, target **api.Error) bool { return errors.As(err, target) }

func mustWriteFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}
