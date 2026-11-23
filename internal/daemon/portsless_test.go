package daemon_test

import (
	"context"
	"strings"
	"testing"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/sdk/api"
)

// A FreeBSD jail with no ports packages has no ports part: neither the update check nor the update fetches the ports
// catalogue into its root. With one ports package the check does.
func TestNoPortsPackagesFetchNoCatalogue(t *testing.T) {
	e := dtest.New(t, dtest.Options{})
	e.Seed()
	sj := &scriptedJails{env: e}
	e.Jails.SetExecFn(sj.fn)
	runningWeb(t, e)
	ctx := context.Background()
	e.Run.Reset()

	cop, err := e.Admin.Updates().Check(ctx, "web")
	if err != nil {
		t.Fatal(err)
	}
	reps, err := cop.Wait(ctx)
	if err != nil || len(reps) != 1 || len(reps[0].Packages) != 0 || strings.Contains(reps[0].Error, "package") && !strings.Contains(reps[0].Error, "base packages") {
		t.Fatalf("%+v %v", reps, err)
	}
	aop, err := e.Admin.Updates().Apply(ctx, api.UpdateApplyRequest{Jails: []string{"web"}, Packages: true})
	if err != nil {
		t.Fatal(err)
	}
	recs, err := aop.Wait(ctx)
	if err != nil || len(recs) != 1 || recs[0].Result != api.UpdateSuccess || recs[0].Note != "no ports packages: nothing to update" {
		t.Fatalf("%+v %v", recs, err)
	}
	if sj.ran("pkg update") || sj.ran("pkg upgrade") {
		t.Fatalf("the ports catalogue was fetched for a jail without ports packages: %v", e.Run.Argv())
	}
	if !sj.ran("pkg query %n") {
		t.Fatal("the package database was not read")
	}

	// one ports package: the catalogue is fetched and the package checked
	e.SetPkgUpgrades(pkgDryRun)
	sj.pkgs = pkgDryRun
	cop, err = e.Admin.Updates().Check(ctx, "web")
	if err != nil {
		t.Fatal(err)
	}
	if reps, err = cop.Wait(ctx); err != nil || len(reps) != 1 || len(reps[0].Packages) != 2 {
		t.Fatalf("%+v %v", reps, err)
	}
	if !sj.ran("pkg update") {
		t.Fatal("the catalogue was not fetched for a jail with ports packages")
	}
}
