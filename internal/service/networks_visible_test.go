package service_test

import (
	"slices"
	"testing"

	"jailops.net/jailops/internal/daemon/dtest"
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

// A network is the host's. The administrator of the host sees every one of them, whatever project the request names (the
// CLI sends the project it was told by JAILOPS_PROJECT or `project switch` with every command, and a network that no
// project is granted yet, such as the one just created, must still be listed). A project identity sees only the
// networks its project is granted.
func TestNetworksOfTheHostAreListedForItsAdministratorInAnyProject(t *testing.T) {
	e, s := newEnv(t)
	spare := e.MakeNetwork("spare") // granted to no project

	for _, project := range []string{"", dtest.SeedProject, "no-such-project"} {
		g := as(globalAdmin, project)
		nets, err := s.ListNetworks(g, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := networkNames(t, nets); !slices.Equal(got, []string{"lan", spare}) {
			t.Errorf("project %q: the administrator lists %v", project, got)
		}
		if _, err := s.GetNetwork(g, spare); err != nil {
			t.Errorf("project %q: get: %v", project, err)
		}
	}

	ann := as(alphaOnly, dtest.SeedProject)
	nets, err := s.ListNetworks(ann, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := networkNames(t, nets); !slices.Equal(got, []string{"lan"}) {
		t.Errorf("a project identity lists %v, want only the network its project is granted", got)
	}
	_, err = s.GetNetwork(ann, spare)
	wantCode(t, err, api.CodeNotFound)
}
