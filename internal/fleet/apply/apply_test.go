package apply

import (
	"bytes"
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"jailops.net/jailops/internal/fleet/config"
	"jailops.net/jailops/internal/fleet/model"
	"jailops.net/jailops/internal/fleet/plan"
	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
	"jailops.net/jailops/sdk/fake"
)

const fleetID = "f1"

// connector hands out SDK clients of fake servers by host name.
type connector struct {
	t     *testing.T
	hosts map[string]*httptest.Server
}

func (c *connector) Connect(ctx context.Context, h *config.Host) (*sdk.Client, error) {
	ts, ok := c.hosts[h.Name]
	if !ok {
		return nil, context.DeadlineExceeded
	}
	return sdk.Connect(ctx, sdk.Remote(ts.URL), sdk.WithBackoff(time.Millisecond, 5*time.Millisecond))
}
func (c *connector) Close() {}

func session(t *testing.T, hosts ...string) (*plan.Session, map[string]*fake.Server) {
	t.Helper()
	f := &config.Fleet{Meta: model.Meta{ID: fleetID}, Hosts: map[string]*config.Host{}}
	conn := &connector{t: t, hosts: map[string]*httptest.Server{}}
	servers := map[string]*fake.Server{}
	for _, h := range hosts {
		f.Hosts[h] = &config.Host{Name: h}
		if h == "down" {
			continue // no server: connecting fails
		}
		srv := fake.New()
		ts := httptest.NewServer(srv)
		t.Cleanup(func() { ts.Close(); srv.Close() })
		conn.hosts[h] = ts
		servers[h] = srv
	}
	return plan.NewSession(f, conn, nil), servers
}

func netItem(host, name string, a plan.Action) plan.Item {
	return plan.Item{Host: host, Kind: api.KindNetwork, Name: name, Action: a,
		Spec: api.NetworkSpec{IPv4: &api.IPv4Net{Subnet: "10.9.0.0/24", Gateway: "10.9.0.1", Range: "10.9.0.10-10.9.0.20"}},
		Meta: api.ObjectMeta{Name: name, ManagedBy: "sync:" + fleetID}}
}

func TestCheckRefusesUnsafePlans(t *testing.T) {
	ok := &plan.Plan{FleetID: fleetID, Hosts: []string{"a"}, Items: []plan.Item{netItem("a", "n", plan.ActionCreate)}}
	if err := Check(ok, Options{}); err != nil {
		t.Fatal(err)
	}
	withErr := &plan.Plan{Items: []plan.Item{{Host: "a", Kind: api.KindNetwork, Name: "n", Action: plan.ActionError, Details: []string{"secret missing"}}}}
	if err := Check(withErr, Options{Force: true}); err == nil || !strings.Contains(err.Error(), "secret missing") {
		t.Fatalf("a plan with an error was accepted: %v", err)
	}
	hostErr := &plan.Plan{Errors: []plan.HostError{{Host: "a", Err: context.DeadlineExceeded}}}
	if err := Check(hostErr, Options{}); err == nil {
		t.Fatal("a plan with an unreachable host was accepted")
	}
	refuse := &plan.Plan{Items: []plan.Item{{Host: "a", Kind: api.KindNetwork, Name: "n", Action: plan.ActionRefuse, Details: []string{"owned by hand"}}}}
	if err := Check(refuse, Options{Force: true}); err == nil || !strings.Contains(err.Error(), "does not own") {
		t.Fatalf("an ownership refusal was overridden by --force: %v", err)
	}
	danger := netItem("a", "n", plan.ActionUpdate)
	danger.Dangers = []string{"changes the subnet"}
	dp := &plan.Plan{Items: []plan.Item{danger}}
	if err := Check(dp, Options{}); err == nil || !strings.Contains(err.Error(), "subnet") {
		t.Fatalf("a dangerous change needs --force: %v", err)
	}
	if err := Check(dp, Options{Force: true}); err != nil {
		t.Fatal(err)
	}
	// a delete that is not executed is not dangerous
	del := netItem("a", "n", plan.ActionDelete)
	del.Dangers = []string{"destroys data"}
	delp := &plan.Plan{Items: []plan.Item{del}}
	if err := Check(delp, Options{Prune: false}); err != nil {
		t.Fatalf("an unexecuted delete was refused: %v", err)
	}
	if err := Check(delp, Options{Prune: true}); err == nil {
		t.Fatal("a pruned dangerous delete needs --force")
	}
}

func TestRunCreatesAndStopsAHostAtItsFirstFailure(t *testing.T) {
	s, srvs := session(t, "a", "down")
	p := &plan.Plan{FleetID: fleetID, Hosts: []string{"a", "down"}, Items: []plan.Item{
		netItem("a", "n1", plan.ActionCreate),
		netItem("a", "n2", plan.ActionCreate),
		netItem("down", "n3", plan.ActionCreate),
		netItem("down", "n4", plan.ActionCreate),
		{Host: "a", Kind: api.KindNetwork, Name: "unrelated", Action: plan.ActionUnmanaged},
	}}
	var out bytes.Buffer
	res, err := Run(context.Background(), s, p, Options{Out: &out})
	if err == nil {
		t.Fatal("an unreachable host must fail the apply")
	}
	if len(res.Failed()) != 1 || res.Failed()[0].Item.Name != "n3" {
		t.Fatalf("failed: %+v", res.Failed())
	}
	status := map[string]string{}
	for _, o := range res.Outcomes {
		status[o.Item.Name] = o.Status
	}
	want := map[string]string{"n1": "ok", "n2": "ok", "n3": "failed", "n4": "skipped"}
	for k, v := range want {
		if status[k] != v {
			t.Errorf("%s: %q, want %q (%v)", k, status[k], v, status)
		}
	}
	if _, ok := status["unrelated"]; ok {
		t.Error("an unmanaged object was touched")
	}
	// the other host went on and the objects carry the ownership marker
	c, _ := s.Client(context.Background(), "a")
	n, err := c.Networks().Get(context.Background(), "n1")
	if err != nil || n.Metadata.ManagedBy != "sync:"+fleetID {
		t.Fatalf("%v %+v", err, n.Metadata)
	}
	_ = srvs
	if !strings.Contains(out.String(), "failed") || !strings.Contains(out.String(), "skipped") {
		t.Errorf("progress output: %q", out.String())
	}
}

func TestRunDeleteNeedsPruneAndOwnership(t *testing.T) {
	ctx := context.Background()
	s, srvs := session(t, "a")
	srvs["a"].SeedNetwork(api.Network{Metadata: api.ObjectMeta{Name: "mine", ManagedBy: "sync:" + fleetID},
		Spec: api.NetworkSpec{IPv4: &api.IPv4Net{Subnet: "10.9.1.0/24", Gateway: "10.9.1.1", Range: "10.9.1.10-10.9.1.20"}}})
	srvs["a"].SeedNetwork(api.Network{Metadata: api.ObjectMeta{Name: "theirs", ManagedBy: "sync:other"},
		Spec: api.NetworkSpec{IPv4: &api.IPv4Net{Subnet: "10.9.2.0/24", Gateway: "10.9.2.1", Range: "10.9.2.10-10.9.2.20"}}})
	p := &plan.Plan{FleetID: fleetID, Hosts: []string{"a"}, Items: []plan.Item{
		netItem("a", "mine", plan.ActionDelete),
	}}
	res, err := Run(ctx, s, p, Options{})
	if err != nil || res.Outcomes[0].Status != "skipped" {
		t.Fatalf("without --prune: %v %+v", err, res.Outcomes)
	}
	c, _ := s.Client(ctx, "a")
	if _, err := c.Networks().Get(ctx, "mine"); err != nil {
		t.Fatal("deleted without --prune")
	}
	// ownership is re-read right before the write: a plan made when the object
	// was ours must not delete it once somebody else owns it
	p = &plan.Plan{FleetID: fleetID, Hosts: []string{"a"}, Items: []plan.Item{netItem("a", "theirs", plan.ActionDelete)}}
	res, err = Run(ctx, s, p, Options{Prune: true})
	if err == nil || res.Outcomes[0].Status != "failed" || !strings.Contains(res.Outcomes[0].Err.Error(), "ownership changed") {
		t.Fatalf("a foreign object was deleted: %v %+v", err, res.Outcomes)
	}
	if _, err := c.Networks().Get(ctx, "theirs"); err != nil {
		t.Fatal("the foreign object is gone")
	}
	p = &plan.Plan{FleetID: fleetID, Hosts: []string{"a"}, Items: []plan.Item{netItem("a", "mine", plan.ActionDelete)}}
	if _, err := Run(ctx, s, p, Options{Prune: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Networks().Get(ctx, "mine"); !api.IsCode(err, api.CodeNotFound) {
		t.Fatalf("prune did not delete: %v", err)
	}
}

func TestRunStopsOnCancelledContextAndRefusedPlan(t *testing.T) {
	s, _ := session(t, "a")
	p := &plan.Plan{FleetID: fleetID, Hosts: []string{"a"}, Items: []plan.Item{netItem("a", "n", plan.ActionCreate)}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Run(ctx, s, p, Options{}); err == nil {
		t.Fatal("a cancelled context still applied")
	}
	bad := &plan.Plan{Items: []plan.Item{{Host: "a", Action: plan.ActionRefuse}}}
	res, err := Run(context.Background(), s, bad, Options{})
	if err == nil || len(res.Outcomes) != 0 {
		t.Fatalf("a refused plan ran: %v %+v", err, res)
	}
}
