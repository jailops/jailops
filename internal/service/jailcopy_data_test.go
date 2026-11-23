package service_test

import (
	"testing"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/sdk/api"
)

// The copy with data is a jail of its own: addresses of its own, no volume, no exposure of the source's ports; the
// source keeps running and keeps its snapshots.
func TestCopyJailWithItsData(t *testing.T) {
	_, s := newEnv(t)
	admin := as(globalAdmin, dtest.SeedProject)
	j := dtest.WebJail("web")
	j.Metadata.Labels = map[string]string{"tier": "front"}
	op, err := s.CreateJail(admin, j)
	mustOp(t, s, op, err)
	op, err = s.CreateSnapshot(admin, "web", api.JailSnapshot{Metadata: api.ObjectMeta{Name: "before"}})
	mustOp(t, s, op, err)

	op, err = s.CopyJail(admin, "web", api.JailCopy{Name: "web2"})
	got := mustOp(t, s, op, err)
	if got.Status != api.OpSuccess {
		t.Fatalf("%+v", got)
	}
	src, _ := s.GetJail(admin, "web")
	cp, err := s.GetJail(admin, "web2")
	if err != nil {
		t.Fatal(err)
	}
	if cp.Metadata.Name != "web2" || cp.Metadata.UUID == src.Metadata.UUID || cp.Metadata.Labels["tier"] != "front" ||
		cp.Spec.Network[0].IPv4 == src.Spec.Network[0].IPv4 || cp.Spec.Network[0].IPv4 == "" || len(cp.Spec.Volumes) != 0 || len(cp.Spec.Expose) != 0 {
		t.Fatalf("the copy: %+v", cp.Spec)
	}
	if cp.Status.Power != api.PowerStopped {
		t.Fatalf("the copy is %s", cp.Status.Power)
	}

	// from a snapshot of the source, with labels of its own
	op, err = s.CopyJail(admin, "web", api.JailCopy{Name: "web3", Snapshot: "before", Labels: map[string]string{"tier": "test"}})
	mustOp(t, s, op, err)
	if cp, err := s.GetJail(admin, "web3"); err != nil || cp.Metadata.Labels["tier"] != "test" {
		t.Fatalf("%v %+v", err, cp.Metadata)
	}
	// the snapshot is still the source's, and the copies have none
	if _, err := s.GetSnapshot(admin, "web", "before"); err != nil {
		t.Fatalf("the snapshot is gone from the source: %v", err)
	}
	if _, err := s.GetSnapshot(admin, "web3", "before"); err == nil {
		t.Fatal("the copy carries the snapshot")
	}

	// a snapshot the source does not have, a name that is taken, a name that is not one, a source that does not exist
	op, err = s.CopyJail(admin, "web", api.JailCopy{Name: "web4", Snapshot: "nope"})
	if err == nil {
		if got := wait(t, s, op); got.Status != api.OpFailure || got.Error == nil || got.Error.Code != api.CodeNotFound {
			t.Fatalf("a snapshot the source does not have: %+v", got)
		}
	} else {
		wantCode(t, err, api.CodeNotFound)
	}
	if _, err := s.GetJail(admin, "web4"); err == nil {
		t.Fatal("a refused copy made a jail")
	}
	_, err = s.CopyJail(admin, "web", api.JailCopy{Name: "web2"})
	wantCode(t, err, api.CodeAlreadyExists)
	_, err = s.CopyJail(admin, "web", api.JailCopy{Name: "Not A Name"})
	wantCode(t, err, api.CodeInvalid)
	_, err = s.CopyJail(admin, "nope", api.JailCopy{Name: "web5"})
	wantCode(t, err, api.CodeNotFound)
}
