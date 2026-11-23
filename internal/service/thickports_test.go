package service_test

import (
	"slices"
	"testing"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/internal/service"
)

// The ports part of an in-place upgrade reads the jail's package database first: a jail without ports packages has
// none, and no ports catalogue is fetched into its root (dry run or real). With a ports package the catalogue is
// fetched and the package planned, as before.
func TestThickPortsSkipsAJailWithoutPortsPackages(t *testing.T) {
	e := dtest.New(t, dtest.Options{})
	e.Seed()
	s := e.D.Service()
	ctx := as(globalAdmin, dtest.SeedProject)
	op, err := s.CreateJail(ctx, dtest.WebJail("web"))
	mustOp(t, s, op, err)
	key := dtest.SeedProject + "_web"
	if err := s.StartJail(ctx, key); err != nil {
		t.Fatal(err)
	}
	fetched := func() bool {
		for _, c := range e.Run.Calls() {
			if c.Cmd.Name == "pkg" && slices.Contains(c.Cmd.Args, "-j") && slices.Contains(c.Cmd.Args, "update") {
				return true
			}
		}
		return false
	}
	e.Run.Reset()
	for _, dry := range []bool{true, false} {
		pending, none, warn := service.ThickPorts(s, ctx, key, dry)
		if !none || len(pending) != 0 || warn != "" {
			t.Fatalf("dry run %v: %v %v %q", dry, pending, none, warn)
		}
	}
	if fetched() {
		t.Fatalf("the ports catalogue was fetched for a jail without ports packages: %v", e.Run.Argv())
	}

	e.SetPkgUpgrades("Installed packages to be UPGRADED:\n\tnginx: 1.24.0_1,3 -> 1.26.0,3\n")
	pending, none, warn := service.ThickPorts(s, ctx, key, false)
	if none || !slices.Equal(pending, []string{"nginx"}) || warn != "" || !fetched() {
		t.Fatalf("%v %v %q %v", pending, none, warn, e.Run.Argv())
	}
}
