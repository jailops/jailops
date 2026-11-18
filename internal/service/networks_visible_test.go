package service_test

import (
	"context"
	"slices"
	"testing"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/internal/service"
	"jailops.net/jailops/sdk/api"
)

func networkNames(t *testing.T, nets []api.Network) []string {
	t.Helper()
	var out []string
	for _, n := range nets {
		out = append(out, n.Metadata.Name)
	}
	slices.Sort(out)
	return out
}

// A network is the host's. The list of a project holds the networks that project is granted, for the administrator of the
// host as for a project identity; the administrator without a project, or asking for everything, lists every one, also
// a network that no project is granted yet (the one just created). A project identity never lists more than its project
// holds, and cannot ask for everything.
func TestNetworksListedAreTheOnesTheProjectIsGranted(t *testing.T) {
	e, s := newEnv(t)
	spare := e.MakeNetwork("spare") // granted to no project

	list := func(ctx context.Context) []string {
		t.Helper()
		nets, err := s.ListNetworks(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		return networkNames(t, nets)
	}
	g := as(globalAdmin, dtest.SeedProject)
	if got := list(g); !slices.Equal(got, []string{"lan"}) {
		t.Errorf("the administrator in a project lists %v, want the network it is granted", got)
	}
	if got := list(as(globalAdmin, "no-such-project")); len(got) != 0 {
		t.Errorf("a project without grants lists %v", got)
	}
	for name, ctx := range map[string]context.Context{
		"no project":         as(globalAdmin, ""),
		"asking for all":     service.WithAllProjects(g),
		"all, other project": service.WithAllProjects(as(globalAdmin, "no-such-project")),
	} {
		if got := list(ctx); !slices.Equal(got, []string{"lan", spare}) {
			t.Errorf("%s: the administrator lists %v", name, got)
		}
	}
	if _, err := s.GetNetwork(g, spare); err != nil {
		t.Errorf("get of a network no project holds: %v", err)
	}

	ann := as(alphaOnly, dtest.SeedProject)
	if got := list(ann); !slices.Equal(got, []string{"lan"}) {
		t.Errorf("a project identity lists %v, want only the network its project is granted", got)
	}
	_, err := s.ListNetworks(service.WithAllProjects(ann), nil)
	wantCode(t, err, api.CodeForbidden)
	_, err = s.GetNetwork(ann, spare)
	wantCode(t, err, api.CodeNotFound)
}

// A network and a storage say the projects they are granted to: all of them for the administrator, the caller's own for a
// project identity.
func TestGrantedToNamesTheProjectsThatHoldTheObject(t *testing.T) {
	e, s := newEnv(t)
	// a network belongs to one project, a storage can be granted to several
	if _, err := e.Admin.Projects().Create(context.Background(), api.Project{Metadata: api.ObjectMeta{Name: "beta"}, Spec: api.ProjectSpec{
		Storage: []api.ProjectStorage{{Storage: "default"}}}}); err != nil {
		t.Fatal(err)
	}
	spare := e.MakeNetwork("spare")

	g := as(globalAdmin, dtest.SeedProject)
	n, err := s.GetNetwork(g, "lan")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{dtest.SeedProject}; !slices.Equal(n.Status.GrantedTo, want) {
		t.Errorf("lan is granted to %v, want %v", n.Status.GrantedTo, want)
	}
	if n, err = s.GetNetwork(g, spare); err != nil || len(n.Status.GrantedTo) != 0 {
		t.Errorf("a network no project holds: %v %v", n.Status.GrantedTo, err)
	}
	st, err := s.GetStorage(g, "default")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{dtest.SeedProject, "beta"}; !slices.Equal(st.Status.GrantedTo, want) {
		t.Errorf("default is granted to %v, want %v", st.Status.GrantedTo, want)
	}
	stores, err := s.ListStorage(service.WithAllProjects(g), nil)
	if err != nil || len(stores) == 0 {
		t.Fatalf("storage list with all: %v %v", stores, err)
	}
	ann := as(alphaOnly, dtest.SeedProject)
	if n, err = s.GetNetwork(ann, "lan"); err != nil || !slices.Equal(n.Status.GrantedTo, []string{dtest.SeedProject}) {
		t.Errorf("a project identity sees %v, want only its own project (%v)", n.Status.GrantedTo, err)
	}
}
