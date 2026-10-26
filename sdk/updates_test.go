package sdk_test

import (
	"testing"

	"jailops.net/jailops/sdk/api"
)

func TestUpdatesAndProvisionAgainstFake(t *testing.T) {
	srv, c := newEnv(t)
	seedBasics(srv)
	spec := webSpec()
	spec.Provision = &api.Provision{Packages: []string{"nginx"}, Services: api.ProvisionSvc{Enable: []string{"nginx"}}}
	mustJail(t, c, "web", spec)
	ctx := ctxT(t)

	// The configuration keys of the update policy and the shell are in the registry.
	op, err := c.Jails().Config(ctx, "web", map[string]string{"updates.mode": "managed", "updates.schedule": "0 3 * * 0", "updates.packages": "true", "shell": "/usr/local/bin/bash"}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	res, err := op.Wait(ctx)
	if err != nil || res.Spec.Update.Mode != api.UpdateModeManaged || !res.Spec.Update.Packages || res.Spec.Shell != "/usr/local/bin/bash" || len(res.RestartRequired) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	// A stopped jail's packages cannot be checked.
	cop, _ := c.Updates().Check(ctx, "web")
	reps, err := cop.Wait(ctx)
	if err != nil || len(reps) != 1 || reps[0].Error == "" || reps[0].Available {
		t.Fatalf("%+v %v", reps, err)
	}
	so, _ := c.Jails().Start(ctx, "web")
	if _, err := so.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	srv.SeedPackageUpdates("web", "web", []api.PackageUpdate{{Name: "curl", From: "8.5", To: "8.6"}})
	cop, _ = c.Updates().Check(ctx)
	reps, _ = cop.Wait(ctx)
	if len(reps) != 1 || !reps[0].Available || reps[0].Packages[0].Name != "curl" {
		t.Fatalf("%+v", reps)
	}
	if l, err := c.Updates().List(ctx); err != nil || len(l) != 1 {
		t.Fatalf("%+v %v", l, err)
	}
	srv.SeedUpdateFailure("web", "web", true)
	aop, _ := c.Updates().Apply(ctx, api.UpdateApplyRequest{Jails: []string{"web"}})
	recs, err := aop.Wait(ctx)
	if err != nil || len(recs) != 1 || recs[0].Result != api.UpdateFailed {
		t.Fatalf("%+v %v", recs, err)
	}
	srv.SeedUpdateFailure("web", "web", false)
	aop, _ = c.Updates().Apply(ctx, api.UpdateApplyRequest{})
	recs, _ = aop.Wait(ctx)
	if len(recs) != 1 || recs[0].Result != api.UpdateOK {
		t.Fatalf("%+v", recs)
	}
	h, err := c.Jails().UpdateHistory(ctx, "web")
	if err != nil || len(h) != 2 || h[0].Result != api.UpdateOK || h[1].Result != api.UpdateFailed {
		t.Fatalf("%+v %v", h, err)
	}
	if _, err := c.Updates().Check(ctx, "ghost"); !api.IsCode(err, api.CodeNotFound) {
		t.Fatal(err)
	}

	// Provisioning runs once, again on request.
	pop, err := c.Jails().Provision(ctx, "web", false)
	if err != nil {
		t.Fatal(err)
	}
	pr, err := pop.Wait(ctx)
	if err != nil || pr.ProvisionedAt.IsZero() || len(pr.Steps) != 2 {
		t.Fatalf("%+v %v", pr, err)
	}
	if j, _ := c.Jails().Get(ctx, "web"); j.Status.ProvisionedAt.IsZero() {
		t.Fatal("not marked")
	}
	if _, err := c.Jails().Provision(ctx, "web", false); !api.IsCode(err, api.CodeAlreadyExists) {
		t.Fatalf("%v", err)
	}
	if pop, err = c.Jails().Provision(ctx, "web", true); err != nil {
		t.Fatal(err)
	}
	if _, err := pop.Wait(ctx); err != nil {
		t.Fatal(err)
	}
}
