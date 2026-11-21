package render

import (
	"net/netip"
	"strings"
)

// The block of /etc/hosts that JailOps owns. Everything outside the two lines is the administrator's.
const (
	HostsBegin = "# BEGIN JailOps (the name of this jail and its addresses; edit outside this block)"
	HostsEnd   = "# END JailOps"
)

// HostsBlock is the block that makes the jail's own name resolve to its own addresses (sudo and many daemons wait for a
// DNS timeout when it does not). It is empty when the jail has no address.
func HostsBlock(host string, addrs []netip.Addr) string {
	if host == "" || len(addrs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(HostsBegin + "\n")
	for _, a := range addrs {
		b.WriteString(a.String() + "\t" + host + "\n")
	}
	b.WriteString(HostsEnd + "\n")
	return b.String()
}

// HostsMerge puts block into existing: it replaces a block that is there, else it is added at the end. An empty block
// removes the old one. The text outside the block stays byte for byte.
func HostsMerge(existing, block string) string {
	lines := strings.SplitAfter(existing, "\n")
	var out []string
	in, found := false, false
	for _, l := range lines {
		t := strings.TrimRight(l, "\r\n")
		switch {
		case !in && strings.HasPrefix(t, "# BEGIN JailOps"):
			in, found = true, true
			out = append(out, block)
		case in && strings.HasPrefix(t, HostsEnd):
			in = false
		case in:
		default:
			out = append(out, l)
		}
	}
	res := strings.Join(out, "")
	if !found && block != "" {
		if res != "" && !strings.HasSuffix(res, "\n") {
			res += "\n"
		}
		res += block
	}
	return res
}
