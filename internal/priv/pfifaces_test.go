package priv

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"jailops.net/jailops/internal/sys/ifconfig"
	"jailops.net/jailops/sdk/api"
)

// A rule of an anchor decides for the whole host: the helper loads one only when it stands on an interface of JailOps, or
// on the uplink of a network in the shapes of a translation and a forward.
func TestAnchorRulesStandOnJailInterfacesOnly(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, _, err := e.net.EpairCreate(ctx, "jo0123456789a0"); err != nil {
		t.Fatal(err)
	}
	nw, _ := json.Marshal(map[string]any{"kind": "Network", "metadata": api.ObjectMeta{Name: "lan", UUID: "aaaa-1111-2222"},
		"spec": api.NetworkSpec{Uplink: "em0", IPv4: &api.IPv4Net{Subnet: "10.20.0.0/24", Gateway: "10.20.0.1", Range: "10.20.0.10-10.20.0.20"}}})
	mustWrite(t, filepath.Join(e.cfg.StateDir, "objects", "Network", "aaaa-1111-2222.json"), string(nw))
	p := e.local.PF()
	const jif = "jo0123456789a0a"
	for _, rs := range []string{
		"pass in quick on " + jif + " all\n",
		"table <host> persist { 192.0.2.1 }\nblock drop in quick on " + jif + " from any to <host>\n",
		"match in on " + jif + " all dnpipe (40001, 59999)\nmatch out on " + jif + " all dnpipe (59999, 40001)\n",
		"match in on " + jif + " all dnpipe (40001, 0)\nmatch out on " + jif + " all dnpipe (0, 40001)\n",
		"match out on em0 inet from 10.20.0.0/24 to ! 10.20.0.0/24 nat-to (em0:0)\n",
		"match in on em0 inet proto tcp from any to (em0) port 8080 rdr-to 10.20.0.10 port 80\npass in quick on em0 inet proto tcp from any to 10.20.0.10 port 80\n",
		"anchor \"*\"\n",
	} {
		if err := p.Load(ctx, "jailops/web/db", rs); err != nil {
			t.Errorf("%q: %v", rs, err)
		}
	}
	for _, rs := range []string{
		"pass quick on em0 all\n",                                             // the uplink, any traffic
		"block drop in quick on em0 from any to any\n",                        // closes the host
		"pass in quick on em0 inet proto tcp from any to any\n",               // names no destination
		"pass in quick on em0 inet proto tcp from any to 127.0.0.1 port 22\n", // an address of the host
		"pass in quick on lo0 all\n",                                          // not JailOps's
		"pass in quick on mybr all\n",                                         // a bridge of the administrator's
		"pass all\n",                                                          // on no interface
		"block drop all\n",                                                    // on no interface
		"match in on " + jif + " all dnpipe 1\n",                              // a pipe outside JailOps's range
		"match out on " + jif + " all dnpipe (0, 1)\n",                        // the reverse pipe is outside the range as well
		"antispoof for em0\n",                                                 // not a rule the renderer writes
		"pass in quick on { " + jif + ", em0 } all\n",                         // a list that holds the uplink
	} {
		err := p.Load(ctx, "jailops/web/db", rs)
		if !api.IsCode(err, api.CodeForbidden) {
			t.Errorf("%q was not refused: %v", rs, err)
		} else if strings.Contains(err.Error(), "block drop") || strings.Contains(err.Error(), "127.0.0.1 port") {
			t.Errorf("the error quotes the ruleset, which may hold what must not reach the audit log: %v", err)
		}
	}
	// the operator may allow raw rulesets (admin-raw-pf)
	cfg := e.cfg
	cfg.Admin.RawPF = true
	l2, _ := NewLocal(cfg, Deps{PF: e.pf, Net: e.net}, WithoutChown())
	if err := l2.PF().Load(ctx, "jailops/web/db", "pass quick on em0 all\n"); err != nil {
		t.Errorf("a raw ruleset the operator allowed: %v", err)
	}
	// the anchor of a jail never stands on the link of another jail, with or without admin-raw-pf
	for _, c := range []struct{ name, desc string }{{"jo0123456789b0", "jailops jail=shop/db net=lan (host side)"}, {"jo0123456789c0", "jailops jail=web/db net=lan (host side)"}} {
		if _, _, err := e.net.EpairCreate(ctx, c.name); err != nil {
			t.Fatal(err)
		}
		if err := e.net.SetDescription(ctx, c.name+"a", c.desc); err != nil {
			t.Fatal(err)
		}
	}
	for _, l := range []*Local{e.local, l2} {
		if err := l.PF().Load(ctx, "jailops/web/db", "pass in quick on jo0123456789c0a all\n"); err != nil {
			t.Errorf("the jail's own link: %v", err)
		}
		if err := l.PF().Load(ctx, "jailops/web/db", "pass in quick on jo0123456789b0a all\n"); !api.IsCode(err, api.CodeForbidden) {
			t.Errorf("the link of another jail was not refused: %v", err)
		}
	}
}

// What the renderer writes on the uplink for the policies of a jail is loaded when it is inbound and names the jail's own
// addresses (one, or a list) as its destination; anything that reaches the host, a gateway or an address outside the stored
// networks, or that is not inbound, is refused.
func TestAnchorRulesOnTheUplinkNameOnlyAJailsAddresses(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	nw, _ := json.Marshal(map[string]any{"kind": "Network", "metadata": api.ObjectMeta{Name: "lan", UUID: "aaaa-1111-2222"},
		"spec": api.NetworkSpec{Uplink: "em0", IPv4: &api.IPv4Net{Subnet: "10.20.0.0/24", Gateway: "10.20.0.1", Range: "10.20.0.10-10.20.0.20"},
			IPv6: &api.IPv6Net{Subnet: "2001:db8:1::/64"}}})
	mustWrite(t, filepath.Join(e.cfg.StateDir, "objects", "Network", "aaaa-1111-2222.json"), string(nw))
	p := e.local.PF()
	for _, rs := range []string{
		"pass in quick on em0 inet6 proto tcp from any to 2001:db8:1::100 port 22 label \"x\" keep state (max 5000, if-bound)\n",
		"pass in log quick on em0 inet proto tcp from <s_office> to { 10.20.0.10, 10.20.0.11 } port { 80, 8000:8100 } keep state (max 5000, if-bound)\n",
		"block drop in quick on em0 inet from urpf-failed to { 10.20.0.10, 10.20.0.11 }\n",
		"block drop in log quick on em0 inet6 proto tcp from 2001:db8:aaaa::bad to 2001:db8:1::100 port 22\n",
	} {
		if err := p.Load(ctx, "jailops/web/db", rs); err != nil {
			t.Errorf("%q: %v", rs, err)
		}
	}
	for _, rs := range []string{
		"pass in quick on em0 inet proto tcp from any to { 10.20.0.10, 127.0.0.1 } port 22\n", // one member is an address of the host
		"pass in quick on em0 inet proto tcp from any to 10.20.0.1 port 22\n",                 // the gateway the host holds for the network
		"pass in quick on em0 inet6 proto tcp from any to 2001:db8:1::1 port 22\n",            // the resolver address the host holds
		"pass in quick on em0 inet proto tcp from any to 192.0.2.9 port 22\n",                 // outside every stored network
		"pass in quick on em0 inet proto tcp from any to <host> port 22\n",                    // a table
		"pass in quick on em0 inet proto tcp from any to { } port 22\n",                       // nothing
		"pass out quick on em0 inet proto tcp from 10.20.0.10 to any port 22\n",               // not inbound
		"block drop in quick on em0 inet proto tcp from any to 10.20.0.1\n",                   // a gateway
		"block drop in quick on em0 from any to any\n",                                        // everything
	} {
		err := p.Load(ctx, "jailops/web/db", rs)
		if !api.IsCode(err, api.CodeForbidden) {
			t.Errorf("%q was not refused: %v", rs, err)
		}
	}
}

// The peers of a WireGuard server of JailOps are an external source: a jail's anchor may name the tunnel's interface in
// the shapes it may name the uplink in, and no other interface that is not JailOps's.
func TestAnchorRulesMayStandOnATunnelOfJailOps(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	nw, _ := json.Marshal(map[string]any{"kind": "Network", "metadata": api.ObjectMeta{Name: "lan", UUID: "aaaa-1111-2222"},
		"spec": api.NetworkSpec{IPv4: &api.IPv4Net{Subnet: "10.20.0.0/24", Gateway: "10.20.0.1", Range: "10.20.0.10-10.20.0.20"}}})
	mustWrite(t, filepath.Join(e.cfg.StateDir, "objects", "Network", "aaaa-1111-2222.json"), string(nw))
	e.net.AddIface(ifconfig.Iface{Name: "wg-office", Groups: []string{"wg"}, Description: ifconfig.Marker})
	e.net.AddIface(ifconfig.Iface{Name: "wg-mine", Groups: []string{"wg"}, Description: "made by hand"}) // not JailOps's
	p := e.local.PF()
	if err := p.Load(ctx, "jailops/web/db", "pass in quick on wg-office inet proto tcp from 10.99.0.0/24 to 10.20.0.10 port 22 keep state (max 5000, if-bound)\n"); err != nil {
		t.Errorf("a pass on the tunnel for a jail's address: %v", err)
	}
	for _, rs := range []string{
		"pass in quick on wg-office inet proto tcp from any to any port 22\n",       // no destination of a jail
		"pass in quick on wg-office inet proto tcp from any to 127.0.0.1 port 22\n", // the host
		"pass in quick on wg-mine inet proto tcp from any to 10.20.0.10 port 22\n",  // a tunnel that is not JailOps's
		"pass out quick on wg-office inet proto tcp from any to 10.20.0.10\n",       // not inbound
	} {
		if err := p.Load(ctx, "jailops/web/db", rs); !api.IsCode(err, api.CodeForbidden) {
			t.Errorf("%q was not refused: %v", rs, err)
		}
	}
}
