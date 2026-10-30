package sdk_test

import (
	"testing"

	"jailops.net/jailops/sdk/api"
	"jailops.net/jailops/sdk/fake"
)

func TestHostIdentityAgainstFake(t *testing.T) {
	ctx := ctxT(t)
	_, c := newEnv(t)
	id, err := c.HostSystem().Identity().Get(ctx)
	if err != nil || id.Kind != api.KindHostIdentity || id.Status.Managed || id.Spec.Resolvers == nil || id.Spec.NTP == nil {
		t.Fatalf("%+v %v", id, err)
	}
	spec := api.HostIdentitySpec{Hostname: "web1.example.org"}
	if _, err := c.HostSystem().Identity().Apply(ctx, spec, true); !api.IsCode(err, api.CodeNotManaged) {
		t.Fatalf("not managed: %v", err)
	}
	if _, err := c.HostSystem().Identity().Apply(ctx, api.HostIdentitySpec{Hostname: "bad name"}, true); !api.IsCode(err, api.CodeInvalid) {
		t.Fatalf("invalid: %v", err)
	}

	_, c = newEnv(t, fake.WithHostManaged())
	h := c.HostSystem()
	dry, err := h.Identity().Apply(ctx, spec, true)
	if err != nil || !dry.DryRun || !dry.Changed || dry.Diff == "" || dry.Snapshot != "" {
		t.Fatalf("%+v %v", dry, err)
	}
	if id, _ := h.Identity().Get(ctx); id.Spec.Hostname == spec.Hostname {
		t.Fatal("the dry run changed the host")
	}
	res, err := h.Identity().Apply(ctx, spec, false)
	if err != nil || res.Snapshot == "" {
		t.Fatalf("%+v %v", res, err)
	}
	if id, _ := h.Identity().Get(ctx); id.Spec.Hostname != spec.Hostname || id.Status.Applied == nil {
		t.Fatalf("%+v", id)
	}
	snaps, err := h.Snapshots(ctx)
	if err != nil || len(snaps) != 1 || snaps[0].Name != res.Snapshot {
		t.Fatalf("%+v %v", snaps, err)
	}
	back, err := h.Restore(ctx, api.HostSnapshotLatest, false)
	if err != nil || !back.Changed {
		t.Fatalf("%+v %v", back, err)
	}
	if id, _ := h.Identity().Get(ctx); id.Spec.Hostname == spec.Hostname {
		t.Fatal("the restore did not put the old name back")
	}
	if _, err := h.Restore(ctx, "../x", true); !api.IsCode(err, api.CodeInvalid) {
		t.Fatal(err)
	}
}
