//go:build linux

package auth

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// PeerCred returns the credentials of the peer of a unix socket connection,
// using SO_PEERCRED. This is the Linux equivalent of the FreeBSD
// implementation and exists so the logic can be tested on the dev machine.
func PeerCred(conn *net.UnixConn) (Cred, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return Cred{}, err
	}
	var uc *unix.Ucred
	var serr error
	if err := raw.Control(func(fd uintptr) {
		uc, serr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return Cred{}, err
	}
	if serr != nil {
		return Cred{}, fmt.Errorf("auth: SO_PEERCRED: %w", serr)
	}
	return Cred{UID: uc.Uid, GID: uc.Gid, PID: uc.Pid}, nil
}
