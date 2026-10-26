//go:build !linux && !freebsd

package auth

import "net"

// PeerCred is not available on this platform.
func PeerCred(*net.UnixConn) (Cred, error) { return Cred{}, ErrPeerCredUnsupported }
