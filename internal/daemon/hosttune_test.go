package daemon_test

import (
	"context"
	"strings"
	"testing"

	"jailops.net/jailops/internal/daemon"
	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/sdk/api"
)

func TestLinuxJailManagesTheFallbackBrandAndModules(t *testing.T) {
	e := dtest.New(t, dtest.Options{})
	e.Seed()
	ctx := context.Background()
	// Without a Linux jail the brand stays as it is and no Linux module is wanted.
	e.D.Reconciler().Pass(ctx)
	if v := e.SysctlValue("kern.elf64.fallback_brand"); v != "-1" {
		t.Fatalf("the brand was set without a Linux jail: %s", v)
	}
	e.SeedLinux()
	// The image build sets the brand for its own jail. Once the build is over nothing needs it, and the reconcile loop
	// (which runs beside this test, kicked by the finished build) may give it back at any moment: so ask whether it was
	// ever set, not what it is now.
	hist := e.SysctlHistory("kern.elf64.fallback_brand")
	set := false
	for _, v := range hist {
		set = set || v == "3"
	}
	if !set {
		t.Fatalf("the Alpine build did not set the brand: %v", hist)
	}
	e.SetSysctl("kern.elf64.fallback_brand", "-1")
	op, err := e.Admin.Jails().Create(ctx, dtest.AlpineJail("alp1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := op.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	e.D.Reconciler().Pass(ctx)
	if v := e.SysctlValue("kern.elf64.fallback_brand"); v != "3" {
		t.Fatalf("brand %s", v)
	}
	rep, _ := e.Admin.Doctor(ctx)
	for _, c := range rep.Checks {
		if c.Name == "sysctl.kern.elf64.fallback_brand" && c.Status != api.DoctorOK {
			t.Fatalf("%+v", c)
		}
	}
	// The bridge sysctls are the configured defaults and the helper never touched /etc/sysctl.conf.
	for k, want := range map[string]string{"net.link.bridge.pfil_bridge": "0", "net.link.bridge.pfil_member": "1", "net.link.bridge.pfil_onlyip": "1"} {
		if v := e.SysctlValue(k); v != want {
			t.Errorf("%s=%s", k, v)
		}
	}
	if _, ok := e.Files.Data["/etc/sysctl.conf"]; ok {
		t.Error("the daemon wrote /etc/sysctl.conf")
	}
	// The jail's mounts and the compat directory.
	if _, ok := e.Files.Dirs["/jailops/projects/alpha/jails/alp1/root/dev/shm"]; !ok {
		t.Errorf("no dev/shm in the root: %v", e.Files.Dirs)
	}
	// The last Linux jail goes away: the brand goes back.
	del, err := e.Admin.Jails().Delete(ctx, "alp1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := del.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	e.D.Reconciler().Pass(ctx)
	if v := e.SysctlValue("kern.elf64.fallback_brand"); v != "-1" {
		t.Fatalf("brand %s after the last Linux jail", v)
	}
}

func TestNetworkSysctlConfigIsExactAndOverridesDefaults(t *testing.T) {
	for _, bad := range []string{"network:\n  sysctl:\n    net.inet.ip.forwarding: 1\n", "network:\n  sysctl:\n    net.link.bridge.pfil_member: 2\n", "network:\n  sysctl:\n    net.link.bridge.pfil_member: 0\n", "network:\n  pf_placement: bridge\n"} {
		if _, err := daemon.Decode(strings.NewReader("pool: tank\n" + bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	e := dtest.New(t, dtest.Options{Mutate: func(c *daemon.Config) {
		c.Network.Sysctl = map[string]string{"net.link.bridge.pfil_bridge": "1", "net.link.bridge.pfil_onlyip": "0"}
	}})
	e.Seed()
	e.D.Reconciler().Pass(context.Background())
	for k, want := range map[string]string{"net.link.bridge.pfil_bridge": "1", "net.link.bridge.pfil_member": "1", "net.link.bridge.pfil_onlyip": "0"} {
		if v := e.SysctlValue(k); v != want {
			t.Errorf("%s=%s want %s", k, v, want)
		}
	}
}

func TestDoctorWarnsWhenForwardingIsNotPersistent(t *testing.T) {
	e := dtest.New(t, dtest.Options{})
	e.Seed()
	ctx := context.Background()
	if _, err := e.Admin.Networks().Patch(ctx, "lan", map[string]any{"spec": map[string]any{"uplink": "em0"}}, ""); err != nil {
		t.Fatal(err)
	}
	check := func() api.DoctorCheck {
		rep, _ := e.Admin.Doctor(ctx)
		for _, c := range rep.Checks {
			if c.Name == "sysctl.persistent" {
				return c
			}
		}
		return api.DoctorCheck{}
	}
	e.Files.Data["/etc/sysctl.conf"] = []byte("# nothing\n")
	if c := check(); c.Status != api.DoctorWarn || !strings.Contains(c.Hint, "never edits") {
		t.Fatalf("%+v", c)
	}
	e.Files.Data["/etc/sysctl.conf"] = []byte("net.inet.ip.forwarding=1\n")
	if c := check(); c.Status != api.DoctorOK {
		t.Fatalf("%+v", c)
	}
	// the FreeBSD way, rc.conf, counts as well
	e.Files.Data["/etc/sysctl.conf"] = []byte("# nothing\n")
	e.Files.Data["/etc/rc.conf"] = []byte("gateway_enable=\"YES\"\n")
	if c := check(); c.Status != api.DoctorOK {
		t.Fatalf("gateway_enable in rc.conf was not accepted: %+v", c)
	}
	e.Files.Data["/etc/rc.conf"] = []byte("gateway_enable=\"NO\"\n")
	if c := check(); c.Status != api.DoctorWarn {
		t.Fatalf("%+v", c)
	}
}
