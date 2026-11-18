package server_test

import (
	"testing"

	"jailops.net/jailops/sdk"
	"jailops.net/jailops/sdk/api"
)

// An IPv4 forward opens a port of the host to the network: only a global administrator makes one, whether the jail is
// created with it or exposed afterwards. A project administrator exposes what needs no forward (IPv6, routed IPv4).
func TestPortForwardsNeedAGlobalAdministrator(t *testing.T) {
	e := seeded(t)
	c := ctxT(t)
	op := e.Scoped(api.PermOperator, "alpha")
	adm := e.Scoped(api.PermAdmin, "alpha")
	if _, err := e.Admin.Networks().Patch(c, "lan", map[string]any{"spec": map[string]any{"uplink": "em0", "ipv4": map[string]any{"nat": true}}}, ""); err != nil {
		t.Fatal(err)
	}
	sp := webSpec()
	sp.Expose = []api.ExposeEntry{{Proto: "tcp", Ports: []string{"22"}, Families: []string{"4"}, HostPort: 2222}}
	create := func(cl *sdk.Client, name string, sp api.JailSpec) error {
		o, err := cl.Jails().Create(c, sdk.NewJail(name, sp))
		if err == nil {
			_, err = o.Wait(c)
		}
		return err
	}
	if err := create(op, "fwd", sp); !api.IsCode(err, api.CodeForbidden) {
		t.Fatalf("an operator made a forward: %v", err)
	}
	if err := create(adm, "fwd", sp); !api.IsCode(err, api.CodeForbidden) {
		t.Fatalf("an administrator of the project made a forward: %v", err)
	}
	if err := create(e.Admin, "fwd", sp); err != nil {
		t.Fatalf("a global administrator could not: %v", err)
	}
	// the same through expose on a jail that exists
	if err := create(e.Admin, "plain", webSpec()); err != nil {
		t.Fatal(err)
	}
	req := api.ExposeRequest{Ports: []string{"80"}}
	if _, err := op.Jails().Expose(c, "plain", req); !api.IsCode(err, api.CodeForbidden) {
		t.Errorf("an operator exposed a port: %v", err)
	}
	if _, err := adm.Jails().Expose(c, "plain", req); !api.IsCode(err, api.CodeForbidden) {
		t.Errorf("an administrator of the project made a forward: %v", err)
	}
	if _, err := e.Admin.Jails().Expose(c, "plain", req); err != nil {
		t.Errorf("a global administrator could not: %v", err)
	}
	if _, err := adm.Jails().Unexpose(c, "plain", api.UnexposeRequest{}); !api.IsCode(err, api.CodeForbidden) {
		t.Errorf("an administrator of the project removed a forward: %v", err)
	}
	// the keys of the old forms are not keys any more
	if _, err := e.Admin.Jails().Config(c, "plain", map[string]string{"proxy.ssh.listen": ":2223", "proxy.ssh.jail_port": "22"}, nil, false); err == nil {
		t.Error("the proxy key is accepted")
	}
}
