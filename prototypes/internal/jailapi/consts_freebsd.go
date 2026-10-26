//go:build freebsd

package jailapi

import "golang.org/x/sys/unix"

// Consts lists the x/sys constants this prototype depends on. Compiling this file is itself the
// proof that they exist for the build target; the values printed on the host are the second check.
func Consts() map[string]uint64 {
	return map[string]uint64{
		"SYS_JAIL_SET":    unix.SYS_JAIL_SET,
		"SYS_JAIL_GET":    unix.SYS_JAIL_GET,
		"SYS_JAIL_REMOVE": unix.SYS_JAIL_REMOVE,
		"SYS_JAIL_ATTACH": unix.SYS_JAIL_ATTACH,
		"LOCAL_PEERCRED":  unix.LOCAL_PEERCRED,
		"SOL_LOCAL":       unix.SOL_LOCAL,
		"SizeofXucred":    unix.SizeofXucred,
	}
}
