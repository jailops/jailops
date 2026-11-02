package server_test

import (
	"slices"
	"strings"
	"testing"

	"jailops.net/jailops/internal/daemon"
	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/sdk/api"
)

func fwTestSpec() api.HostFirewallSpec {
	return api.HostFirewallSpec{Template: api.HostFirewallCustom, ICMP: true,
		SSH:     &api.HostFirewallSSH{AllowFrom: []string{"127.0.0.0/8", "192.0.2.0/24"}},
		Console: &api.HostFirewallConsole{AllowFrom: []string{"127.0.0.1"}},
		Allow:   []api.HostFirewallAllow{{Proto: "tcp", Ports: []string{"443"}, Comment: "web"}}}
}

func fwEnv(t *testing.T, helper, config bool) *dtest.Env {
	var mods []string
	if helper {
		mods = []string{"firewall"}
	}
	return dtest.New(t, dtest.Options{HostManage: mods, Mutate: func(cfg *daemon.Config) {
		cfg.Listen.HTTPS.Address = "127.0.0.1:39110"
		if config {
			cfg.Host.Manage.Modules = []string{"firewall"}
		}
	}})
}

// Off by default: reading works, a change answers not_managed and pf.conf is not touched.
func TestHostFirewallNotManaged(t *testing.T) {
	for _, sides := range [][2]bool{{false, false}, {true, false}, {false, true}} {
		e := fwEnv(t, sides[0], sides[1])
		c := ctxT(t)
		info, _ := e.Admin.Info(c)
		if !hasStr(info.APIExtensions, "host_firewall") || !hasStr(info.APIExtensions, "host_pending") {
			t.Fatalf("extensions %v", info.APIExtensions)
		}
		fw, err := e.Client(api.PermReadonly).HostSystem().Firewall().Get(c)
		if err != nil || fw.Kind != api.KindHostFirewall || fw.Status.Managed || fw.Spec.Template != "default" || fw.Status.ConsolePort != 39110 ||
			!fw.Status.FileExists || fw.Status.FileManaged || len(fw.Status.Rules) == 0 {
			t.Fatalf("%+v %v", fw, err)
		}
		_, err = e.Admin.HostSystem().Firewall().Apply(c, api.HostFirewallApply{Spec: fwTestSpec(), DryRun: true})
		wantCode(t, err, api.CodeNotManaged)
		if e.Host.File("pf.conf") != dtest.HostPF {
			t.Error("pf.conf changed")
		}
		ck := doctorCheck(t, e, "host.firewall")
		if sides[0] == sides[1] && (ck.Status != api.DoctorOK || !strings.Contains(ck.Message, "not managed")) {
			t.Errorf("%+v", ck)
		}
		if sides[0] != sides[1] && ck.Status != api.DoctorWarn {
			t.Errorf("one side: %+v", ck)
		}
		if p, err := e.Admin.HostSystem().Pending(c); err != nil || p.Pending != nil {
			t.Errorf("%+v %v", p, err)
		}
	}
}

func TestHostFirewallManaged(t *testing.T) {
	e := fwEnv(t, true, true)
	c := ctxT(t)
	fwc := e.Admin.HostSystem().Firewall()
	// an operator may not, nor confirm
	_, err := e.Client(api.PermOperator).HostSystem().Firewall().Apply(c, api.HostFirewallApply{Spec: fwTestSpec(), DryRun: true})
	wantCode(t, err, api.CodeForbidden)
	_, err = e.Client(api.PermOperator).HostSystem().Confirm(c, "")
	wantCode(t, err, api.CodeForbidden)
	// the console port is the daemon's own
	bad := fwTestSpec()
	bad.Console.Port = 8443
	_, err = fwc.Apply(c, api.HostFirewallApply{Spec: bad, DryRun: true})
	wantCode(t, err, api.CodeInvalid)
	_, err = fwc.Apply(c, api.HostFirewallApply{Spec: fwTestSpec(), ConfirmWithin: 5})
	wantCode(t, err, api.CodeInvalid)

	// the test client comes from 127.0.0.1: a spec that admits neither ssh nor the console from there is refused
	locked := fwTestSpec()
	locked.SSH.AllowFrom, locked.Console.AllowFrom = []string{"192.0.2.0/24"}, []string{"192.0.2.7"}
	_, err = fwc.Apply(c, api.HostFirewallApply{Spec: locked})
	var ae *api.Error
	if !asErr(err, &ae) || ae.Code != api.CodeForbidden || ae.Detail["guard"] != "lockout" || !strings.Contains(ae.Message, "127.0.0.1") {
		t.Fatalf("lockout: %v", err)
	}

	dry, err := fwc.Apply(c, api.HostFirewallApply{Spec: fwTestSpec(), DryRun: true, ConfirmWithin: 30})
	if err != nil || !dry.DryRun || !strings.Contains(dry.Diff, "+pass in proto tcp from 127.0.0.1 to (self) port 39110") || len(dry.Rules) == 0 {
		t.Fatalf("%+v %v", dry, err)
	}
	if e.Host.File("pf.conf") != dtest.HostPF {
		t.Fatal("the dry run wrote")
	}
	res, err := fwc.Apply(c, api.HostFirewallApply{Spec: fwTestSpec(), ConfirmWithin: 30})
	if err != nil || res.Pending == nil || res.Snapshot == "" || res.Pending.ConfirmWithin != 30 {
		t.Fatalf("%+v %v", res, err)
	}
	if !strings.HasPrefix(e.Host.File("pf.conf"), "# managed by jailops\n") || e.Host.PFLoaded() != e.Host.File("pf.conf") {
		t.Fatal("not written and loaded")
	}
	p, err := e.Client(api.PermReadonly).HostSystem().Pending(c)
	if err != nil || p.Pending == nil || p.Pending.ID != res.Pending.ID || p.Pending.Module != "firewall" {
		t.Fatalf("%+v %v", p, err)
	}
	fw, _ := fwc.Get(c)
	if fw.Status.Pending == nil || !fw.Status.Managed || fw.Status.Applied == nil {
		t.Errorf("%+v", fw.Status)
	}
	if ck := doctorCheck(t, e, "host.firewall"); ck.Status != api.DoctorWarn || !strings.Contains(ck.Message, "waits for its confirmation") {
		t.Errorf("%+v", ck)
	}
	// busy while it waits, naming it
	_, err = fwc.Apply(c, api.HostFirewallApply{Spec: fwTestSpec()})
	if !asErr(err, &ae) || ae.Code != api.CodeBusy || ae.Detail["pending"] != res.Pending.ID {
		t.Errorf("busy: %v", err)
	}
	// the deadline passes (the helper's timer): back to the administrator's file
	if !e.Host.FireTimer() {
		t.Fatal("no timer armed")
	}
	if e.Host.File("pf.conf") != dtest.HostPF || e.Host.PFLoaded() != dtest.HostPF {
		t.Fatal("not reverted")
	}
	p, _ = e.Admin.HostSystem().Pending(c)
	if p.Pending != nil || p.Last == nil || p.Last.Outcome != api.HostPendingTimedOut {
		t.Fatalf("%+v", p)
	}
	// again, and confirm
	res, err = fwc.Apply(c, api.HostFirewallApply{Spec: fwTestSpec()})
	if err != nil {
		t.Fatal(err)
	}
	if p, err = e.Admin.HostSystem().Confirm(c, res.Pending.ID); err != nil || p.Last.Outcome != api.HostPendingConfirmed {
		t.Fatalf("%+v %v", p, err)
	}
	if e.Host.FireTimer() {
		t.Error("a timer after the confirmation")
	}
	if ck := doctorCheck(t, e, "host.firewall"); ck.Status != api.DoctorOK {
		t.Errorf("%+v", ck)
	}
	// a hand edit is drift
	mustWriteFile(t, e.Host.Etc+"/pf.conf", e.Host.File("pf.conf")+"pass in quick all\n")
	if fw, _ := fwc.Get(c); !slices.Equal(fw.Status.Drift, []string{"pf.conf"}) {
		t.Errorf("drift %v", fw.Status.Drift)
	}
	if ck := doctorCheck(t, e, "host.firewall"); ck.Status != api.DoctorWarn || !strings.Contains(ck.Message, "pf.conf") {
		t.Errorf("%+v", ck)
	}
	// a revert through the API
	res, err = fwc.Apply(c, api.HostFirewallApply{Spec: api.HostFirewallSpec{Template: "open"}})
	if err != nil {
		t.Fatal(err)
	}
	if p, err = e.Admin.HostSystem().Revert(c, ""); err != nil || p.Last.Outcome != api.HostPendingReverted {
		t.Fatalf("%+v %v", p, err)
	}
	if !strings.HasSuffix(e.Host.File("pf.conf"), "pass in quick all\n") {
		t.Error("the revert did not put back the file as it was")
	}
	// the first snapshot puts the administrator's file back, byte for byte
	snaps, _ := e.Admin.HostSystem().Snapshots(c)
	first := snaps[len(snaps)-1]
	if first.Module != "firewall" || first.Reason != "adopt" {
		t.Fatalf("%+v", snaps)
	}
	if _, err := e.Admin.HostSystem().Restore(c, first.Name, false); err != nil {
		t.Fatal(err)
	}
	if e.Host.File("pf.conf") != dtest.HostPF || e.Host.File("rc.conf") != dtest.HostRC {
		t.Errorf("%q", e.Host.File("pf.conf"))
	}

	entries, err := e.Admin.Audit(c, 100)
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
	for _, want := range []string{"PUT /api/v1/host/firewall ok dry_run", "PUT /api/v1/host/firewall ok snapshot=", "PUT /api/v1/host/firewall denied",
		"POST /api/v1/host/pending/confirm ok pending=", "POST /api/v1/host/pending/revert ok pending="} {
		if !strings.Contains(all, want) {
			t.Errorf("the audit log lacks %q:\n%s", want, all)
		}
	}
}
