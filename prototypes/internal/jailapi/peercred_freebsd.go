//go:build freebsd

package jailapi

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// Cred is the peer credential of a unix socket connection.
type Cred struct {
	Uid    uint32
	Groups []uint32
}

// PeerCred reads LOCAL_PEERCRED from a unix connection.
// UNVERIFIED: unix.GetsockoptXucred(fd, SOL_LOCAL, LOCAL_PEERCRED) works on an accepted socket
// and reports the credentials of the connecting process taken at connect(2) time.
func PeerCred(c *net.UnixConn) (Cred, error) {
	sc, err := c.SyscallConn()
	if err != nil {
		return Cred{}, err
	}
	var cred Cred
	var gerr error
	err = sc.Control(func(fd uintptr) {
		x, e := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if e != nil {
			gerr = e
			return
		}
		if x.Version != 0 { // UNVERIFIED: XUCRED_VERSION is 0
			gerr = fmt.Errorf("unexpected xucred version %d", x.Version)
		}
		cred.Uid = x.Uid
		for i := 0; i < int(x.Ngroups) && i < len(x.Groups); i++ {
			cred.Groups = append(cred.Groups, x.Groups[i])
		}
	})
	if err != nil {
		return Cred{}, err
	}
	return cred, gerr
}
