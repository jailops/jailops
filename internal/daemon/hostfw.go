package daemon

import (
	"net"
	"strconv"
)

// consolePort is the port of the TLS listener, 0 without one: the host firewall module opens it.
func consolePort(addr string) int {
	if addr == "" {
		return 0
	}
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(p)
	if err != nil || n < 0 || n > 65535 {
		return 0
	}
	return n
}
