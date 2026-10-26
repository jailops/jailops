//go:build !freebsd

package jailapi

import (
	"errors"
	"net"
)

// Cred is the peer credential of a unix socket connection.
type Cred struct {
	Uid    uint32
	Groups []uint32
}

// PeerCred is unavailable off FreeBSD.
func PeerCred(c *net.UnixConn) (Cred, error) {
	return Cred{}, errors.New("LOCAL_PEERCRED check runs only on FreeBSD")
}
