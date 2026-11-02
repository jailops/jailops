package sdk_test

import (
	"sync"
	"testing"
	"time"

	"jailops.net/jailops/sdk/api"
	"jailops.net/jailops/sdk/fake"
)

func TestHostFirewallAgainstFake(t *testing.T) {
	ctx := ctxT(t)
	_, c := newEnv(t)
	fw, err := c.HostSystem().Firewall().Get(ctx)
	if err != nil || fw.Kind != api.KindHostFirewall || fw.Status.Managed || fw.Spec.Template != api.HostFirewallDefault {
		t.Fatalf("%+v %v", fw, err)
	}
	if _, err := c.HostSystem().Firewall().Apply(ctx, api.HostFirewallApply{Spec: api.HostFirewallSpec{}, DryRun: true}); !api.IsCode(err, api.CodeNotManaged) {
		t.Fatalf("not managed: %v", err)
	}

	var mu sync.Mutex
	now := time.Date(2026, 10, 30, 10, 15, 0, 0, time.UTC)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	_, c = newEnv(t, fake.WithHostFirewall(), fake.WithClock(clock))
	h := c.HostSystem()
	spec := api.HostFirewallSpec{Template: api.HostFirewallCustom, Allow: []api.HostFirewallAllow{{Proto: "tcp", Ports: []string{"443"}}}}
	if _, err := h.Firewall().Apply(ctx, api.HostFirewallApply{Spec: api.HostFirewallSpec{Template: "permissive"}}); !api.IsCode(err, api.CodeInvalid) {
		t.Fatalf("invalid: %v", err)
	}
	res, err := h.Firewall().Apply(ctx, api.HostFirewallApply{Spec: spec, ConfirmWithin: 30})
	if err != nil || res.Pending == nil || res.Pending.SecondsLeft != 30 {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := h.Firewall().Apply(ctx, api.HostFirewallApply{Spec: api.HostFirewallSpec{}}); !api.IsCode(err, api.CodeBusy) {
		t.Fatalf("busy: %v", err)
	}
	if _, err := h.Confirm(ctx, "not an id"); !api.IsCode(err, api.CodeInvalid) {
		t.Fatalf("id: %v", err)
	}
	p, err := h.Confirm(ctx, res.Pending.ID)
	if err != nil || p.Last == nil || p.Last.Outcome != api.HostPendingConfirmed {
		t.Fatalf("%+v %v", p, err)
	}
	// the next one times out on the fake's clock
	if _, err = h.Firewall().Apply(ctx, api.HostFirewallApply{Spec: api.HostFirewallSpec{Template: api.HostFirewallOpen}}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	now = now.Add(121 * time.Second)
	mu.Unlock()
	p, err = h.Pending(ctx)
	if err != nil || p.Pending != nil || p.Last.Outcome != api.HostPendingTimedOut {
		t.Fatalf("%+v %v", p, err)
	}
	if fw, _ := h.Firewall().Get(ctx); fw.Spec.Template != api.HostFirewallCustom {
		t.Errorf("the timeout kept %s", fw.Spec.Template)
	}
	if _, err := h.Revert(ctx, ""); !api.IsCode(err, api.CodeNotFound) {
		t.Errorf("nothing to revert: %v", err)
	}
}
