package zfs

import (
	"context"
	"testing"

	"jailops.net/jailops/internal/sys/run"
	"jailops.net/jailops/sdk/api"
)

func TestFeatureState(t *testing.T) {
	ctx := context.Background()
	r := run.NewFake()
	r.On("zpool", []string{"get", "-H", "-p", "-o", "value", "feature@encryption", "tank"}, "enabled\n")
	r.On("zpool", []string{"get", "-H", "-p", "-o", "value", "feature@nope", "tank"}, "-\n")
	c := NewClient(r)
	if st, err := c.FeatureState(ctx, "tank", "encryption"); err != nil || st != FeatureEnabled {
		t.Fatalf("%q %v", st, err)
	}
	if st, err := c.FeatureState(ctx, "tank", "nope"); err != nil || st != "" {
		t.Fatalf("an unknown feature: %q %v", st, err)
	}
	for _, bad := range []string{"", "Encryption", "a b", "feature@x", "-o"} {
		if _, err := c.FeatureState(ctx, "tank", bad); !api.IsCode(err, api.CodeInvalid) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	f := NewFake("tank")
	if st, _ := f.FeatureState(ctx, "tank", "encryption"); st != FeatureEnabled {
		t.Fatal(st)
	}
	f.SetFeatureState("tank", "encryption", FeatureDisabled)
	if st, _ := f.FeatureState(ctx, "tank", "encryption"); st != FeatureDisabled {
		t.Fatal(st)
	}
	f.SetFeatures("tank", "encryption")
	if st, _ := f.FeatureState(ctx, "tank", "encryption"); st != FeatureActive {
		t.Fatal(st)
	}
}
