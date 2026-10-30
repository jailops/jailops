package e2e

import (
	"context"
	"testing"

	"jailops.net/jailops/sdk/api"
)

const wgServer = `apiVersion: jailops/v1
kind: WireGuard
metadata:
  name: office
spec:
  mode: server
  address: [10.99.0.1/24, "fd99::1/64"]
  networks: [lan]
  peers:
    - {name: laptop, public_key: AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=, allowed_ips: [10.99.0.2/32]}
`

// A server tunnel declared with the public parts of its peers applies, plans to zero (the defaults the daemon fills
// in and the peers' creation times are not drift), and a peer enrolled on the host outside the file is a dangerous
// change that sync removes only with --force.
func TestWireGuardServerSyncs(t *testing.T) {
	e := newEnv(t, "all", "h1")
	e.Write("shared/networks/lan.yaml", "apiVersion: jailops/v1\nkind: Network\nmetadata:\n  name: lan\nspec:\n  ipv4: {subnet: 10.20.0.0/24, gateway: 10.20.0.1, range: 10.20.0.2-10.20.0.254}\n")
	e.Write("hosts/h1/wireguard/office.yaml", wgServer)
	mustOK(t, run(t, e, "validate"))
	mustOK(t, run(t, e, "sync", "--apply"))
	r := mustOK(t, run(t, e, "plan"))
	contains(t, r.out, "0 create, 0 update, 0 delete")

	c := client(t, e.Hosts["h1"])
	ctx := context.Background()
	w, err := c.WireGuard().Get(ctx, "office")
	if err != nil {
		t.Fatal(err)
	}
	if w.Metadata.ManagedBy != "sync:all" || w.Spec.Interface != "wg-office" || w.Spec.ListenPort != api.DefaultWireGuardPort || len(w.Spec.Peers) != 1 {
		t.Fatalf("%+v", w)
	}
	if _, err := c.WireGuard().AddPeer(ctx, "office", api.WireGuardPeerAdd{Name: "phone"}); err != nil {
		t.Fatal(err)
	}
	r = mustOK(t, run(t, e, "plan"))
	contains(t, r.out, "1 update")
	mustFail(t, run(t, e, "sync", "--apply"), "dangerous changes need --force")
	mustOK(t, run(t, e, "sync", "--apply", "--force"))
	if w, err = c.WireGuard().Get(ctx, "office"); err != nil || len(w.Spec.Peers) != 1 || w.Spec.Peers[0].Name != "laptop" {
		t.Fatalf("%+v %v", w.Spec.Peers, err)
	}
	mustOK(t, run(t, e, "plan"))
}
