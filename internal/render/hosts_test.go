package render

import (
	"net/netip"
	"strings"
	"testing"
)

func TestHostsBlockAndMerge(t *testing.T) {
	a := []netip.Addr{netip.MustParseAddr("10.0.0.2"), netip.MustParseAddr("2001:db8::2")}
	block := HostsBlock("web", a)
	if !strings.Contains(block, "10.0.0.2\tweb\n") || !strings.Contains(block, "2001:db8::2\tweb\n") {
		t.Fatal(block)
	}
	if HostsBlock("web", nil) != "" || HostsBlock("", a) != "" {
		t.Fatal("a jail without an address or a name has no block")
	}
	own := "::1\tlocalhost\n127.0.0.1\tlocalhost\n"
	first := HostsMerge(own, block)
	if !strings.HasPrefix(first, own) || !strings.HasSuffix(first, block) {
		t.Fatalf("appended after the administrator's lines:\n%s", first)
	}
	if again := HostsMerge(first, block); again != first {
		t.Fatalf("the same block twice changes nothing:\n%s", again)
	}
	changed := HostsMerge(first, HostsBlock("web", a[:1]))
	if strings.Contains(changed, "2001:db8::2") || !strings.HasPrefix(changed, own) {
		t.Fatalf("a new block replaces the old:\n%s", changed)
	}
	if HostsMerge(first, "") != own {
		t.Fatalf("an empty block removes it: %q", HostsMerge(first, ""))
	}
	mid := "a\n" + block + "b\n"
	if got := HostsMerge(mid, block); got != mid {
		t.Fatalf("text after the block stays:\n%s", got)
	}
	if got := HostsMerge("no newline", block); !strings.HasPrefix(got, "no newline\n"+HostsBegin) {
		t.Fatalf("%q", got)
	}
}
