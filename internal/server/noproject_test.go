package server_test

import (
	"strings"
	"testing"

	"jailops.net/jailops/internal/daemon/dtest"
	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

// There is no default project: a request for something that lives in a project names the project, and the host-wide
// kinds need none.
func TestProjectLevelRoutesNeedAProject(t *testing.T) {
	e := seeded(t)
	tok := e.Token(api.PermAdmin)
	for _, rt := range [][2]string{
		{"GET", "/1.0/jails"}, {"POST", "/1.0/jails"}, {"GET", "/1.0/jails/web"}, {"GET", "/1.0/profiles"}, {"GET", "/1.0/firewall/policies"},
		{"GET", "/1.0/firewall/address-sets"}, {"GET", "/1.0/backup-policies"}, {"POST", "/1.0/restores"},
	} {
		code, body := e.Raw(rt[0], rt[1], tok, "{}")
		if code != 400 || !strings.Contains(body, `"code":"invalid"`) || !strings.Contains(body, "a project is required") || !strings.Contains(body, "--project NAME") {
			t.Errorf("%s %s without a project: %d %s", rt[0], rt[1], code, body)
		}
	}
	for _, p := range []string{"/1.0/networks", "/1.0/images", "/1.0/projects", "/1.0/storage", "/1.0/profiles?project=host"} {
		if code, body := e.Raw("GET", p, tok, ""); code != 200 {
			t.Errorf("GET %s: %d %s", p, code, body)
		}
	}
	// Authorization comes first: a caller who may not read does not learn what a missing project would say.
	if code, _ := e.Raw("GET", "/1.0/jails", "", ""); code != 401 {
		t.Errorf("anonymous: %d", code)
	}
	// A client with no project says so without sending anything.
	_, err := e.Global().Jails().Get(ctxT(t), "web")
	wantCode(t, err, api.CodeInvalid)
}

// A jail names its network and its storage, itself or through a profile; nothing is filled in.
func TestAJailNeedsANetworkAndAStorage(t *testing.T) {
	e := seeded(t)
	c := ctxT(t)
	bare := api.JailSpec{Image: "fb@stable", Storage: "default"}
	_, err := e.Admin.Jails().Create(c, sdk.NewJail("a", bare))
	wantCode(t, err, api.CodeInvalid)
	if !strings.Contains(err.Error(), "a jail needs a network") {
		t.Fatalf("%v", err)
	}
	bare = api.JailSpec{Image: "fb@stable", Network: []api.NetworkAttach{{Network: "lan"}}}
	_, err = e.Admin.Jails().Create(c, sdk.NewJail("a", bare))
	wantCode(t, err, api.CodeInvalid)
	if !strings.Contains(err.Error(), "a jail needs a storage") {
		t.Fatalf("%v", err)
	}
	// A network device that names no network is refused too.
	_, err = e.Admin.Jails().Create(c, sdk.NewJail("a", api.JailSpec{Image: "fb@stable", Storage: "default", Network: []api.NetworkAttach{{Name: "eth0"}}}))
	wantCode(t, err, api.CodeInvalid)

	// A profile can carry several networks and the storage.
	second := e.MakeNetwork("second")
	pr, _ := e.Admin.Projects().Get(c, dtest.SeedProject)
	pr.Spec.Networks = append(pr.Spec.Networks, api.ProjectNetwork{Network: second})
	if _, err := e.Admin.Projects().Update(c, dtest.SeedProject, pr, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Admin.Profiles().Create(c, api.Profile{Metadata: api.ObjectMeta{Name: "dual"}, Spec: api.ProfileSpec{Config: map[string]string{
		"nic.eth0.network": "lan", "nic.eth1.network": second, "storage": "default"}}}); err != nil {
		t.Fatal(err)
	}
	op, err := e.Admin.Jails().Create(c, sdk.NewJail("viaprofile", api.JailSpec{Image: "fb@stable", Profiles: []string{"dual"}}))
	if err != nil {
		t.Fatal(err)
	}
	j, err := op.Wait(c)
	if err != nil {
		t.Fatalf("%v\n%s", err, e.Log())
	}
	if j.Spec.Storage != "default" {
		t.Fatalf("%+v", j.Spec)
	}
	ex, err := e.Admin.Jails().Expanded(c, "viaprofile")
	if err != nil || len(ex.Jail.Spec.Network) != 2 {
		t.Fatalf("%v %+v", err, ex.Jail.Spec.Network)
	}
}

// A network belongs to one project.
func TestANetworkBelongsToOneProject(t *testing.T) {
	e := seeded(t)
	c := ctxT(t)
	_, err := e.Admin.Projects().Create(c, api.Project{Metadata: api.ObjectMeta{Name: "other"}, Spec: api.ProjectSpec{
		Networks: []api.ProjectNetwork{{Network: "lan"}}}})
	wantCode(t, err, api.CodeConflict)
	if !strings.Contains(err.Error(), "network lan is already granted to project "+dtest.SeedProject) {
		t.Fatalf("%v", err)
	}
	if _, err := e.Admin.Projects().Create(c, api.Project{Metadata: api.ObjectMeta{Name: "other"}}); err != nil {
		t.Fatal(err)
	}
	o, _ := e.Admin.Projects().Get(c, "other")
	o.Spec.Networks = []api.ProjectNetwork{{Network: "lan"}}
	_, err = e.Admin.Projects().Update(c, "other", o, "")
	wantCode(t, err, api.CodeConflict)
	// a wildcard grant would give every network to one project
	o.Spec.Networks = []api.ProjectNetwork{{Network: "*"}}
	_, err = e.Admin.Projects().Update(c, "other", o, "")
	wantCode(t, err, api.CodeInvalid)
	// After the first project gives the network up, another can have it.
	pr, _ := e.Admin.Projects().Get(c, dtest.SeedProject)
	pr.Spec.Networks = nil
	if _, err := e.Admin.Projects().Update(c, dtest.SeedProject, pr, ""); err != nil {
		t.Fatal(err)
	}
	o.Spec.Networks = []api.ProjectNetwork{{Network: "lan"}}
	if _, err := e.Admin.Projects().Update(c, "other", o, ""); err != nil {
		t.Fatal(err)
	}
}
