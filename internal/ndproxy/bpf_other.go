//go:build !freebsd

package ndproxy

import (
	"net/netip"
	"os"

	"jailops.net/jailops/sdk/api"
)

func unsupported(what string) error {
	return api.Errorf(api.CodeUnsupported, "%s needs FreeBSD", what)
}

// BPF is the FreeBSD BPF capture and injector. On other platforms every
// method returns api.CodeUnsupported.
type BPF struct{ iface string }

// NewBPF returns a Capture and Injector for the uplink.
func NewBPF(iface string) *BPF { return &BPF{iface: iface} }

// Link implements Capture.
func (b *BPF) Link() (LinkInfo, error) { return SystemLink(b.iface) }

// Open implements Capture.
func (b *BPF) Open(LinkInfo, bool) (Stream, error) { return nil, unsupported("BPF capture") }

// Send implements Injector.
func (b *BPF) Send([]byte) error { return unsupported("BPF injection") }

// NewFileStream is unsupported here.
func NewFileStream(*os.File) (Stream, error) { return nil, unsupported("BPF capture") }

// MulticastJoiner joins solicited-node groups. Unsupported here.
type MulticastJoiner struct{}

// NewMulticastJoiner returns a Joiner.
func NewMulticastJoiner() *MulticastJoiner { return &MulticastJoiner{} }

// Join implements Joiner.
func (*MulticastJoiner) Join(string, netip.Addr) error { return unsupported("multicast join") }

// Leave implements Joiner.
func (*MulticastJoiner) Leave(string, netip.Addr) error { return unsupported("multicast leave") }

// Close releases all memberships.
func (*MulticastJoiner) Close() error { return nil }
