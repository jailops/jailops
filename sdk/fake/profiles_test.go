package fake_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
	"jailops.net/jailops/sdk/fake"
)

func TestFakeConfigAndProfiles(t *testing.T) {
	s := fake.New()
	defer s.Close()
	webProject(s)
	s.SeedImage(api.Image{Metadata: api.ObjectMeta{Name: "img"}})
	c, err := sdk.Connect(context.Background(), sdk.Remote(serve(t, s)), sdk.WithProject("web"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	keys, err := c.ConfigKeys(ctx)
	if err != nil || len(keys) < 20 {
		t.Fatalf("%v %d", err, len(keys))
	}
	if _, err := c.Profiles().Create(ctx, api.Profile{Metadata: api.ObjectMeta{Name: "big"}, Spec: api.ProfileSpec{Config: map[string]string{"limits.memory": "8G", "limits.cpu": "4"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Profiles().Create(ctx, api.Profile{Metadata: api.ObjectMeta{Name: "bad"}, Spec: api.ProfileSpec{Config: map[string]string{"hostname": "x"}}}); !api.IsCode(err, api.CodeInvalid) {
		t.Fatalf("hostname in a profile: %v", err)
	}
	op, err := c.Jails().Create(ctx, sdk.NewJail("web", api.JailSpec{Image: "img", Profiles: []string{"big"}, Resources: api.Resources{CPU: 2}, Storage: "default", Network: []api.NetworkAttach{{Network: "lan"}}}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := op.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	ex, err := c.Jails().Expanded(ctx, "web")
	if err != nil || ex.Jail.Spec.Resources.Memory != "8G" || ex.Jail.Spec.Resources.CPU != 2 || ex.Sources["limits.memory"] != "big" || ex.Sources["limits.cpu"] != "jail" {
		t.Fatalf("%v %+v", err, ex)
	}
	cop, err := c.Jails().Config(ctx, "web", map[string]string{"limits.memory": "1G", "boot.priority": "3"}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	res, err := cop.Wait(ctx)
	if err != nil || res.Spec.Resources.Memory != "1G" || res.Spec.Boot.Priority != 3 || len(res.RestartRequired) != 1 || res.RestartRequired[0] != "limits.memory" {
		t.Fatalf("%v %+v", err, res)
	}
	// A profile can supply the network and the storage; without them a jail is refused, and nothing is defaulted.
	if _, err := c.Jails().Create(ctx, sdk.NewJail("bare", api.JailSpec{Image: "img", Storage: "default"})); !api.IsCode(err, api.CodeInvalid) || !strings.Contains(err.Error(), "a jail needs a network") {
		t.Fatalf("a jail without a network: %v", err)
	}
	if _, err := c.Jails().Create(ctx, sdk.NewJail("bare", api.JailSpec{Image: "img", Network: []api.NetworkAttach{{Network: "lan"}}})); !api.IsCode(err, api.CodeInvalid) || !strings.Contains(err.Error(), "a jail needs a storage") {
		t.Fatalf("a jail without a storage: %v", err)
	}
	if _, err := c.Profiles().Create(ctx, api.Profile{Metadata: api.ObjectMeta{Name: "placed"}, Spec: api.ProfileSpec{Config: map[string]string{"nic.eth0.network": "lan", "storage": "default"}}}); err != nil {
		t.Fatal(err)
	}
	pop, err := c.Jails().Create(ctx, sdk.NewJail("viaprofile", api.JailSpec{Image: "img", Profiles: []string{"placed"}}))
	if err != nil {
		t.Fatalf("network and storage from a profile: %v", err)
	}
	if _, err := pop.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.Profiles().Delete(ctx, "big"); !api.IsCode(err, api.CodeBusy) {
		t.Fatalf("a used profile was deleted: %v", err)
	}
	cop, _ = c.Jails().Config(ctx, "web", map[string]string{"limits.ram": "1"}, nil, false)
	if cop != nil {
		if _, err := cop.Wait(ctx); !api.IsCode(err, api.CodeInvalid) {
			t.Fatal(err)
		}
	}
}

func serve(t *testing.T, s *fake.Server) string {
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	return ts.URL
}
