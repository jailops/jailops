package priv

import (
	"context"
	"strings"
	"testing"

	"jailops.net/jailops/sdk/api"
)

// net.set_description writes only the descriptions JailOps makes: "jailops ..." (or the marker of earlier
// releases), at most 64 characters of a fixed set, on an interface JailOps owns. Clearing is allowed.
func TestSetDescriptionOverTheWire(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	n := f.client.Net()
	if err := n.BridgeCreate(ctx, "jo0"); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"jailops net=lan bridge", "jailops jail=web/nginx net=lan (host side)", "jailops:0b0e7f3e-5c4b-4f6e-9d3e-111111111111", ""} {
		if err := n.SetDescription(ctx, "jo0", d); err != nil {
			t.Errorf("%q: %v", d, err)
		}
	}
	for _, d := range []string{"uplink", "jailops a;b", "jailops a\nb", "jailops \"x\"", "jailops $(id)", "jailops `id`", "jailops " + strings.Repeat("x", 57), "jailopsx", "JAILOPS net=lan", "jailops ä"} {
		if err := n.SetDescription(ctx, "jo0", d); !api.IsCode(err, api.CodeInvalid) {
			t.Errorf("%q: %v", d, err)
		}
	}
	if err := n.SetDescription(ctx, "em0", "jailops net=lan bridge"); !api.IsCode(err, api.CodeForbidden) {
		t.Errorf("a foreign interface: %v", err)
	}
}
