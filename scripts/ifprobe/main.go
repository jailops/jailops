//go:build freebsd

// ifprobe asks the running helper, over its socket, for the interface operations a compromised daemon could ask for,
// and checks that the helper refuses every one on an interface JailOps did not create (docs/security-model.md,
// "Interfaces"), and accepts what JailOps needs. It is a test tool only, for scripts/ci-freebsd-e2e.sh: built for
// FreeBSD and run as root on a disposable machine, after the end-to-end flow has created the network OWNED and the
// managed jail JAIL. Nothing in JailOps itself uses it.
//
//	ifprobe -uplink vtnet0 -foreign mybr -bare jo79 -owned office -jail edge_edgeweb
//
// FOREIGN is a bridge made by hand (ifconfig bridge create; ifconfig bridgeN name mybr), BARE one made by hand
// under a jo name without a description: the helper refuses it. Every check prints one line; the exit
// status is 1 when one failed.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/netip"
	"os"
	"strings"

	"jailops.net/jailops/internal/priv"
	"jailops.net/jailops/internal/sys/ifconfig"
	"jailops.net/jailops/internal/sys/run"
	"jailops.net/jailops/sdk/api"
)

var failed int

func check(name string, ok bool, detail any) {
	if ok {
		fmt.Printf("ok   %s\n", name)
		return
	}
	failed++
	fmt.Printf("FAIL %s: %v\n", name, detail)
}

// refused: the helper said forbidden or invalid, never yes and never something else.
func refused(name string, err error) {
	check(name+" is refused", err != nil && (api.IsCode(err, api.CodeForbidden) || api.IsCode(err, api.CodeInvalid)), err)
}

func main() {
	sock := flag.String("socket", priv.DefaultSocket, "the helper's socket")
	uplink := flag.String("uplink", "", "the host's uplink")
	foreign := flag.String("foreign", "", "a bridge made by hand")
	bare := flag.String("bare", "", "a bridge made by hand under a jo name, without a description")
	owned := flag.String("owned", "", "the bridge of a JailOps network")
	jail := flag.String("jail", "", "a running managed jail (its internal name)")
	flag.Parse()
	if *uplink == "" || *foreign == "" || *bare == "" || *owned == "" || *jail == "" {
		flag.Usage()
		os.Exit(2)
	}
	c, err := priv.Dial(*sock)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	defer c.Close()
	ctx := context.Background()
	n := c.Net()
	desc := func(name string) string {
		list, err := n.List(ctx)
		if err != nil {
			return "error: " + err.Error()
		}
		for _, i := range list {
			if i.Name == name {
				return i.Description
			}
		}
		return "(missing)"
	}
	before := map[string]string{}
	for _, name := range []string{*uplink, "lo0", *foreign} {
		before[name] = desc(name)
	}

	// 1. nothing that is not JailOps's is changed; the requests are harmless if they were carried out by mistake
	for _, name := range []string{*uplink, "lo0", *foreign} {
		refused("destroy "+name, n.BridgeDestroy(ctx, name))
		refused("destroy (epair) "+name, n.EpairDestroy(ctx, name))
		refused("link up "+name, n.SetLink(ctx, name, true))
		refused("address on "+name, n.AddrAdd(ctx, name, netip.MustParsePrefix("192.0.2.77/32")))
		refused("the marker written on "+name, n.SetDescription(ctx, name, ifconfig.Marker))
		refused("a JailOps text written on "+name, n.SetDescription(ctx, name, "jailops net=x bridge"))
		refused(name+" made a port of "+*owned, n.BridgeAddMember(ctx, *owned, name, false))
		refused("route through "+name, n.RouteAdd(ctx, ifconfig.Route{Dest: "2001:db8::77/128", Interface: name}))
		refused("neighbor entry on "+name, n.NeighborSet(ctx, name, netip.MustParseAddr("2001:db8::77"), "02:00:00:00:00:77"))
		refused("mtu of "+name, c.SetMTU(ctx, name, 1400))
		refused("move "+name+" into "+*jail, n.MoveToJail(ctx, name, *jail))
	}
	refused("a port added to "+*foreign, n.BridgeAddMember(ctx, *foreign, *owned+"ga", false))
	for name, was := range before {
		check("the description of "+name+" is unchanged", desc(name) == was, desc(name))
	}

	// 2. a name that exists is never taken over; a driver's name is never created
	err = n.BridgeCreate(ctx, *foreign)
	check("create over "+*foreign+" is refused as someone else's", api.IsCode(err, api.CodeForbidden) && strings.Contains(err.Error(), "never takes over"), err)
	for _, name := range []string{*uplink, "vmx0", "em0", "lo1", "bridge0", "bridge", "tap0", "wg0", "pflog1", "vlan0.10"} {
		err := n.BridgeCreate(ctx, name)
		check("create "+name+" is refused as a driver's name", api.IsCode(err, api.CodeInvalid) && strings.Contains(err.Error(), "form of the interfaces of the driver"), err)
	}
	if _, _, err := n.EpairCreate(ctx, "epair9"); !api.IsCode(err, api.CodeInvalid) {
		check("epair epair9a/epair9b is refused", false, err)
	} else {
		check("epair epair9a/epair9b is refused", true, nil)
	}
	err = n.BridgeCreate(ctx, *owned)
	check("create of "+*owned+" again answers already_exists", api.IsCode(err, api.CodeAlreadyExists), err)

	// 3. no command line makes or renames a host interface
	for _, argv := range [][]string{{"bridge", "create"}, {"epair", "create"}, {"bridge0", "name", "jo5"}, {*uplink, "name", "jo5"}, {*foreign, "destroy"}} {
		_, err := c.Runner().Run(ctx, run.Cmd{Name: "ifconfig", Args: argv})
		refused("ifconfig "+strings.Join(argv, " ")+" through the runner", err)
	}
	_, err = c.Runner().Run(ctx, run.Cmd{Name: "ifconfig", Args: []string{"-j", *jail, "lo0", "down"}})
	refused("ifconfig -j "+*jail+" lo0 down", err)

	// 4. a bridge without a description is not JailOps's, whatever its name
	check(*bare+" has no description", desc(*bare) == "", desc(*bare))
	refused(*bare+" is refused", n.SetLink(ctx, *bare, true))
	check(*bare+" is still undescribed", desc(*bare) == "", desc(*bare))

	// 5. what JailOps makes carries the marker from its creation, in the jail's vnet too
	a, b, err := n.EpairCreate(ctx, "joprobe")
	check("epair joprobea/joprobeb is created", err == nil && a == "joprobea" && b == "joprobeb", err)
	check("joprobea carries the marker", desc("joprobea") == ifconfig.Marker, desc("joprobea"))
	check("joprobeb moves into "+*jail, n.MoveToJail(ctx, b, *jail) == nil, nil)
	out, err := c.Runner().Run(ctx, run.Cmd{Name: "ifconfig", Args: []string{"-j", *jail, b}})
	check("joprobeb keeps the marker in the jail's vnet", err == nil && strings.Contains(string(out.Stdout), "description: "+ifconfig.Marker), string(out.Stdout))
	_, err = c.Runner().Run(ctx, run.Cmd{Name: "ifconfig", Args: []string{"-j", *jail, b, "up"}})
	check("ifconfig -j "+*jail+" joprobeb up", err == nil, err)
	check("joprobeb comes back", n.MoveFromJail(ctx, b, *jail) == nil, nil)
	check("the pair is destroyed", n.EpairDestroy(ctx, a) == nil, nil)
	refused("a description that drops the marker", n.SetDescription(ctx, *owned, "my bridge"))

	if failed > 0 {
		fmt.Printf("%d checks failed\n", failed)
		os.Exit(1)
	}
	fmt.Println("all checks passed")
}
