package e2e

import (
	"net/http/httptest"
	"testing"

	"jailops.net/jailops/internal/fleet/fleettest"
	"jailops.net/jailops/sdk/fake"
)

// Hosts reached through a hub: every request carries ?host=<name> and the hub
// forwards it, so one connection serves the whole fleet.
func TestHostsThroughAHub(t *testing.T) {
	h1, h2 := fake.New(fake.WithHostName("h1")), fake.New(fake.WithHostName("h2"))
	hub := fake.New(fake.WithHosts(map[string]*fake.Server{"h1": h1, "h2": h2}))
	ts := httptest.NewServer(hub)
	t.Cleanup(func() { ts.Close(); hub.Close(); h1.Close(); h2.Close() })

	e := fleettest.New(t, "hubfleet")
	e.Write("fleet.yaml", "id: hubfleet\nhub: {url: "+ts.URL+"}\n")
	for _, h := range []string{"h1", "h2"} {
		e.Write("hosts/"+h+"/host.yaml", "apiVersion: jailops/v1\nkind: Host\nmetadata:\n  name: "+h+"\nspec:\n  via_hub: true\n")
	}
	e.StandardObjects()
	e.Jail("h1", "blog", "tier: web", "")
	mustOK(t, run(t, e, "sync", "--apply"))
	if _, ok := h1.JailIn(fleettest.Project, "blog"); !ok {
		t.Fatal("jail missing on h1")
	}
	if _, ok := h2.Network("internal"); !ok {
		t.Fatal("network missing on h2")
	}
	if _, ok := h2.JailIn(fleettest.Project, "blog"); ok {
		t.Fatal("jail leaked to h2")
	}
	mustOK(t, run(t, e, "drift"))
	r := mustOK(t, run(t, e, "doctor"))
	contains(t, r.out, "ok    host h1")
}
