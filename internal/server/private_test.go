package server_test

import (
	"strings"
	"testing"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/internal/render"
	"jailops.net/jailops/sdk/api"
)

// Every jail port is a private bridge member whatever the network says: jails reach each other through the host.
func TestEveryJailPortIsPrivate(t *testing.T) {
	e := dtest.New(t, dtest.Options{})
	e.Seed()
	c := ctxT(t)
	n := dtest.LAN()
	n.Metadata.Name = "priv"
	n.Spec.IPv4 = &api.IPv4Net{Subnet: "10.30.0.0/24", Gateway: "10.30.0.1", Range: "10.30.0.10-10.30.0.50"}
	n.Spec.Uplink = "em0"
	if _, err := e.Admin.Networks().Create(c, n); err != nil {
		t.Fatal(err)
	}
	// A private network belongs to one project, like every network: no sharing, so no ranges to keep apart.
	if _, err := e.Admin.Projects().Create(c, api.Project{Metadata: api.ObjectMeta{Name: "p1"}, Spec: api.ProjectSpec{
		Networks: []api.ProjectNetwork{{Network: "priv"}},
		Storage:  []api.ProjectStorage{{Storage: "default"}}}}); err != nil {
		t.Fatal(err)
	}
	_, err := e.Admin.Projects().Create(c, api.Project{Metadata: api.ObjectMeta{Name: "p2"}, Spec: api.ProjectSpec{
		Networks: []api.ProjectNetwork{{Network: "priv"}}}})
	if !api.IsCode(err, api.CodeConflict) {
		t.Fatalf("a second project took the private network: %v", err)
	}
	// Jails attach privately: the host end becomes a private member.
	jl := dtest.WebJail("w1")
	jl.Spec.Network = []api.NetworkAttach{{Network: "priv", IPv4: "auto"}}
	jl.Spec.Storage = "default"
	op, err := e.Admin.Project("p1").Jails().Create(c, jl)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := op.Wait(c); err != nil {
		t.Fatal(err)
	}
	so, err := e.Admin.Project("p1").Jails().Start(c, "w1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := so.Wait(c); err != nil {
		t.Fatal(err)
	}
	des, err := e.D.Service().Desired(c)
	if err != nil {
		t.Fatal(err)
	}
	var private []string
	for _, jd := range des.Jails {
		for _, steps := range jd.Attach {
			for _, st := range steps {
				if st.Op == render.OpSetPrivate {
					private = append(private, st.Describe())
				}
			}
		}
	}
	if len(private) != 1 || !strings.HasPrefix(private[0], "ifconfig jo") || !strings.Contains(private[0], " private jo") {
		t.Fatalf("the attach plan lacks `ifconfig <bridge> private <epair>`: %v", private)
	}
}
