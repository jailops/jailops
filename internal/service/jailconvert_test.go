package service_test

import (
	"encoding/json"
	"testing"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/sdk/api"
)

func convertResult(t *testing.T, op api.Operation) api.JailConvertResult {
	t.Helper()
	var r api.JailConvertResult
	if err := json.Unmarshal(op.Metadata, &r); err != nil {
		t.Fatalf("%v: %s", err, op.Metadata)
	}
	return r
}

// A jail changes layout and back; the plan of a dry run is a result and changes nothing; the same layout is no change.
func TestConvertJailLayout(t *testing.T) {
	_, s := newEnv(t)
	admin := as(globalAdmin, dtest.SeedProject)
	op, err := s.CreateJail(admin, dtest.WebJail("web"))
	mustOp(t, s, op, err)
	waitShellSetup(t, s, admin, "web")
	before, _ := s.GetJail(admin, "web")
	if before.Spec.Layout != api.LayoutThin {
		t.Fatalf("layout %q", before.Spec.Layout)
	}

	op, err = s.ConvertJail(admin, "web", api.JailConvert{Layout: api.LayoutThick, DryRun: true})
	got := mustOp(t, s, op, err)
	if r := convertResult(t, got); !r.DryRun || !r.Changed || r.From != api.LayoutThin || r.To != api.LayoutThick || len(r.Steps) == 0 {
		t.Fatalf("%+v", r)
	}
	if j, _ := s.GetJail(admin, "web"); j.Spec.Layout != api.LayoutThin {
		t.Fatalf("a dry run changed the layout: %q", j.Spec.Layout)
	}
	op, err = s.ConvertJail(admin, "web", api.JailConvert{Layout: api.LayoutThin})
	if r := convertResult(t, mustOp(t, s, op, err)); r.Changed {
		t.Fatalf("%+v", r)
	}

	op, err = s.ConvertJail(admin, "web", api.JailConvert{Layout: api.LayoutThick})
	got = mustOp(t, s, op, err)
	if got.Status != api.OpSuccess {
		t.Fatalf("%+v", got)
	}
	if j, _ := s.GetJail(admin, "web"); j.Spec.Layout != api.LayoutThick {
		t.Fatalf("layout %q", j.Spec.Layout)
	}
	_, err = s.ConvertJail(admin, "nope", api.JailConvert{Layout: api.LayoutThin})
	wantCode(t, err, api.CodeNotFound)
	op, err = s.ConvertJail(admin, "web", api.JailConvert{Layout: "fat"})
	if err == nil {
		if r := wait(t, s, op); r.Status != api.OpFailure || r.Error == nil || r.Error.Code != api.CodeInvalid {
			t.Fatalf("%+v", r)
		}
	} else {
		wantCode(t, err, api.CodeInvalid)
	}
}
