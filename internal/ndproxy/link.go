package ndproxy

import (
	"net"

	"jailops.net/jailops/sdk/api"
)

// SystemLink reports the current state of the named interface using the
// standard library. It works on every platform.
func SystemLink(name string) (LinkInfo, error) {
	ifi, err := net.InterfaceByName(name)
	if err != nil {
		return LinkInfo{}, api.Errorf(api.CodeNotFound, "interface %s: %v", name, err)
	}
	info := LinkInfo{Index: ifi.Index, Up: ifi.Flags&net.FlagUp != 0}
	if len(ifi.HardwareAddr) != 6 {
		return LinkInfo{}, api.Errorf(api.CodeInvalid, "interface %s has no Ethernet address", name)
	}
	copy(info.MAC[:], ifi.HardwareAddr)
	return info, nil
}
