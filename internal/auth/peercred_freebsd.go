//go:build freebsd

package auth

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// PeerCred returns the credentials of the peer of a unix socket connection
// using getsockopt(SOL_LOCAL, LOCAL_PEERCRED), which yields a struct xucred
// (uid and groups; no pid).
//
// UNVERIFIED: not run on FreeBSD. Open questions for design section 16 item 8:
// whether the call works for sockets accepted by the Go runtime, whether
// Ngroups is trustworthy for a peer with more than 16 groups (xucred holds 16),
// and that the credential is that of the process that called connect(2) or
// listen(2), as documented in unix(4).
func PeerCred(conn *net.UnixConn) (Cred, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return Cred{}, err
	}
	var xc *unix.Xucred
	var serr error
	if err := raw.Control(func(fd uintptr) {
		xc, serr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	}); err != nil {
		return Cred{}, err
	}
	if serr != nil {
		return Cred{}, fmt.Errorf("auth: LOCAL_PEERCRED: %w", serr)
	}
	n := int(xc.Ngroups)
	if n < 0 || n > len(xc.Groups) {
		return Cred{}, fmt.Errorf("auth: LOCAL_PEERCRED reported %d groups", xc.Ngroups)
	}
	c := Cred{UID: xc.Uid, Groups: append([]uint32(nil), xc.Groups[:n]...)}
	if n > 0 {
		c.GID = xc.Groups[0]
	}
	return c, nil
}
