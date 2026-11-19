package render

import (
	"fmt"
	"strings"
	"testing"

	"jailops.net/jailops/internal/ipam"
)

// The names JailOps gives to what it makes in pf and on the network are part of what an upgrade must not change: a host that
// is upgraded keeps its anchors, tables, tags and interfaces, and a rule an administrator wrote against one of them keeps
// working. The golden file is the list; a change of a name needs a migration and a release note.
func TestPFAndInterfaceNamesAreStable(t *testing.T) {
	var b strings.Builder
	line := func(f string, a ...any) { fmt.Fprintf(&b, f+"\n", a...) }
	line("anchor.root %s", AnchorRoot)
	line("anchor.shared %s", AnchorShared)
	line("anchor.line %q", AnchorLines())
	line("anchor.project %s", ProjectAnchor("shop"))
	line("anchor.jail %s", JailAnchor("shop_web"))
	line("anchor.jail-no-project %s", JailAnchor("web"))
	line("anchor.network %s", NetAnchor("lan"))
	line("table.host %s", HostTable)
	line("table.allowed-source %s", AllowedSourceTable)
	line("table.foreign %s", ForeignTable)
	line("tag %s", tagName("shop_web-1"))
	for _, i := range []int{0, 1, 35, 36} {
		h, j, err := ipam.EpairNames("0b9e5f2c-6d54-4c1b-9f55-3a8c2f6d1e10", i)
		if err != nil {
			t.Fatal(err)
		}
		line("epair.%d %s %s", i, h, j)
	}
	line("description.host %s", JailIfaceDesc("shop", "web", "lan", true))
	line("description.jail %s", JailIfaceDesc("shop", "web", "lan", false))
	line("description.wireguard %s", WireGuardDesc("office"))
	golden(t, "names", b.String())
}
