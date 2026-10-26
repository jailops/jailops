//go:build !freebsd

package priv

import (
	"net/netip"
	"os"

	"jailops.net/jailops/sdk/api"
)

// systemNDP is not available off FreeBSD; tests inject Deps.NDP.
type systemNDP struct{}

func (systemNDP) Capture(string, bool) (*os.File, error) {
	return nil, api.Errorf(api.CodeUnsupported, "packet capture is only available on FreeBSD")
}
func (systemNDP) Send(string, []byte) error {
	return api.Errorf(api.CodeUnsupported, "packet sending is only available on FreeBSD")
}
func (systemNDP) HardwareAddr(name string) ([6]byte, error)  { return hardwareAddr(name) }
func (systemNDP) LocalAddrs() ([]netip.Addr, error)          { return localAddrs() }
func (systemNDP) HostRoutes() (map[netip.Addr]string, error) { return hostRoutes() }
