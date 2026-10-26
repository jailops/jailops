package auth

import (
	"errors"

	"jailops.net/jailops/sdk/api"
)

// Cred is the identity of the process on the other end of a unix socket, as
// reported by the kernel.
type Cred struct {
	UID uint32
	// GID is the primary group where the platform reports one (Linux); on
	// FreeBSD it is the first entry of Groups.
	GID uint32
	// PID is 0 where the platform does not report it (FreeBSD LOCAL_PEERCRED
	// does not in the xucred version used here).
	PID    int32
	Groups []uint32
}

// ErrPeerCredUnsupported is returned on platforms without a peer credential API.
var ErrPeerCredUnsupported = errors.New("auth: peer credentials are not supported on this platform")

// InGroup reports membership by gid in the primary or supplementary groups.
func (c Cred) InGroup(gid uint32) bool {
	if c.GID == gid {
		return true
	}
	for _, g := range c.Groups {
		if g == gid {
			return true
		}
	}
	return false
}

// RequireUID is the check jailops-priv applies to its single legitimate client:
// the peer must run as one of the allowed uids.
func RequireUID(c Cred, allowed ...uint32) error {
	for _, u := range allowed {
		if c.UID == u {
			return nil
		}
	}
	return api.Errorf(api.CodeForbidden, "peer uid %d is not allowed", c.UID)
}
