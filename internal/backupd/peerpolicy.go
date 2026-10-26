package backupd

import (
	"jailops.net/jailops/internal/auth"
	"jailops.net/jailops/sdk/api"
)

// peerPermission maps the peer of the node's admin socket to a role: root (uid 0) is the administrator, everybody
// else gets nothing. No Unix group is mapped to anything.
func peerPermission(c auth.Cred) api.Permission {
	if c.UID == 0 {
		return api.PermAdmin
	}
	return ""
}
